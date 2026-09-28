// Package artifacts keeps the full plan text and plan JSON of stack results
// in the optional S3 artifact bucket, STACKORDER_ARTIFACT_BUCKET, so that
// Postgres holds only their beginning and a link. The bucket belongs to the
// server's task role and never holds Terraform state.
package artifacts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// DefaultRegion is the region used when neither an option nor the AWS
// configuration names one.
const DefaultRegion = "us-east-1"

// URLScheme prefixes the URLs Put returns.
const URLScheme = "s3://"

// ErrNotFound is returned by Get for a key with no object.
var ErrNotFound = errors.New("artifacts: not found")

// S3Store stores artifacts as objects under a key prefix of one bucket. It
// implements runs.ArtifactStore and is safe for concurrent use.
type S3Store struct {
	client *s3.Client
	bucket string
	prefix string
}

type options struct {
	endpoint   string
	region     string
	creds      aws.CredentialsProvider
	httpClient *http.Client
}

// Option changes how NewS3Store reaches S3.
type Option func(*options)

// WithEndpoint sends every request to endpoint, such as LocalStack's
// http://localhost:4566, with path-style addressing and checksums only
// where S3 requires them, which S3-compatible services expect. Without it
// the SDK's endpoint resolution applies, AWS_ENDPOINT_URL_S3 included.
func WithEndpoint(endpoint string) Option {
	return func(o *options) { o.endpoint = strings.TrimRight(endpoint, "/") }
}

// WithRegion sets the bucket's region, overriding AWS_REGION and the
// shared configuration.
func WithRegion(region string) Option {
	return func(o *options) { o.region = region }
}

// WithStaticCredentials signs requests with a fixed access key instead of
// the SDK's default credential chain.
func WithStaticCredentials(accessKeyID, secretAccessKey string) Option {
	return func(o *options) {
		o.creds = credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, "")
	}
}

// WithHTTPClient sends requests with c.
func WithHTTPClient(c *http.Client) Option {
	return func(o *options) { o.httpClient = c }
}

// NewS3Store returns a store writing to bucket under prefix. The prefix is
// normalised to no leading slash and one trailing slash, so "plans",
// "/plans" and "plans/" are the same prefix and "" stores at the bucket
// root. Credentials and region come from the SDK's default chain (the ECS
// task role in the Fargate deployment) unless an option sets them. It does
// not contact S3.
func NewS3Store(ctx context.Context, bucket, prefix string, opts ...Option) (*S3Store, error) {
	if strings.TrimSpace(bucket) == "" {
		return nil, errors.New("artifacts: a bucket is required")
	}
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	var loads []func(*config.LoadOptions) error
	if o.region != "" {
		loads = append(loads, config.WithRegion(o.region))
	}
	if o.creds != nil {
		loads = append(loads, config.WithCredentialsProvider(o.creds))
	}
	if o.httpClient != nil {
		loads = append(loads, config.WithHTTPClient(o.httpClient))
	}
	cfg, err := config.LoadDefaultConfig(ctx, loads...)
	if err != nil {
		return nil, fmt.Errorf("artifacts: load AWS configuration: %w", err)
	}
	if cfg.Region == "" {
		cfg.Region = DefaultRegion
	}
	client := s3.NewFromConfig(cfg, func(so *s3.Options) {
		if o.endpoint == "" {
			return
		}
		so.BaseEndpoint = aws.String(o.endpoint)
		so.UsePathStyle = true
		so.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		so.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
	})
	return &S3Store{client: client, bucket: bucket, prefix: normalizePrefix(prefix)}, nil
}

func normalizePrefix(prefix string) string {
	p := strings.Trim(prefix, "/")
	if p == "" {
		return ""
	}
	return p + "/"
}

// Bucket returns the bucket the store writes to.
func (s *S3Store) Bucket() string { return s.bucket }

// Prefix returns the normalised key prefix, "" or ending in "/".
func (s *S3Store) Prefix() string { return s.prefix }

// ObjectKey returns the object key key is stored under: the prefix
// followed by key without leading slashes.
func (s *S3Store) ObjectKey(key string) string {
	return s.prefix + strings.TrimLeft(key, "/")
}

// URL returns the s3://bucket/object-key URL of key.
func (s *S3Store) URL(key string) string {
	return URLScheme + s.bucket + "/" + s.ObjectKey(key)
}

func (s *S3Store) validKey(key string) (string, error) {
	if strings.Trim(key, "/") == "" {
		return "", errors.New("artifacts: a key is required")
	}
	return s.ObjectKey(key), nil
}

// Put stores body under key with contentType and returns its s3:// URL.
// An existing object with the same key is replaced.
func (s *S3Store) Put(ctx context.Context, key, contentType string, body []byte) (string, error) {
	objectKey, err := s.validKey(key)
	if err != nil {
		return "", err
	}
	in := &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(objectKey),
		Body:          bytes.NewReader(body),
		ContentLength: aws.Int64(int64(len(body))),
	}
	if contentType != "" {
		in.ContentType = aws.String(contentType)
	}
	if _, err := s.client.PutObject(ctx, in); err != nil {
		return "", fmt.Errorf("artifacts: put s3://%s/%s: %w", s.bucket, objectKey, err)
	}
	return URLScheme + s.bucket + "/" + objectKey, nil
}

// Get returns the body and content type stored under key, and ErrNotFound
// when there is no such object.
func (s *S3Store) Get(ctx context.Context, key string) (body []byte, contentType string, err error) {
	objectKey, err := s.validKey(key)
	if err != nil {
		return nil, "", err
	}
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(objectKey)})
	if err != nil {
		var respErr interface{ HTTPStatusCode() int }
		if errors.As(err, &respErr) && respErr.HTTPStatusCode() == http.StatusNotFound {
			return nil, "", fmt.Errorf("%w: s3://%s/%s", ErrNotFound, s.bucket, objectKey)
		}
		return nil, "", fmt.Errorf("artifacts: get s3://%s/%s: %w", s.bucket, objectKey, err)
	}
	defer func() { _ = out.Body.Close() }()
	body, err = io.ReadAll(out.Body)
	if err != nil {
		return nil, "", fmt.Errorf("artifacts: read s3://%s/%s: %w", s.bucket, objectKey, err)
	}
	return body, aws.ToString(out.ContentType), nil
}
