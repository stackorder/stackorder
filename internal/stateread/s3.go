package stateread

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

const (
	defaultRegion     = "us-east-1"
	roleSessionName   = "stackorder-stateread"
	bucketRegionField = "X-Amz-Bucket-Region"
)

var validRegion = regexp.MustCompile(`^[a-z0-9-]{1,32}$`).MatchString

// Reader reads the outputs of the state objects its grants allow. It is
// safe for concurrent use and runs at most MaxConcurrent reads at a time.
type Reader struct {
	client *s3.Client
	region string
	grants []Grant
	slots  chan struct{}
}

type options struct {
	endpoint   string
	region     string
	roleARN    string
	creds      aws.CredentialsProvider
	httpClient *http.Client
}

// Option changes how New reaches S3.
type Option func(*options)

// WithEndpoint sends every S3 and STS request to endpoint, such as
// LocalStack's http://localhost:4566, with path-style addressing and
// checksums only where S3 requires them. Without it the SDK's endpoint
// resolution applies, AWS_ENDPOINT_URL_S3 included.
func WithEndpoint(endpoint string) Option {
	return func(o *options) { o.endpoint = strings.TrimRight(endpoint, "/") }
}

// WithRegion sets the region used for a Location without one and for STS,
// overriding AWS_REGION and the shared configuration.
func WithRegion(region string) Option {
	return func(o *options) { o.region = region }
}

// WithRoleARN reads state objects as the role roleARN, assumed through STS
// with the base credentials and refreshed before it expires.
func WithRoleARN(roleARN string) Option {
	return func(o *options) { o.roleARN = roleARN }
}

// WithStaticCredentials signs requests, or the role assumption when
// WithRoleARN is set, with a fixed access key instead of the SDK's default
// credential chain.
func WithStaticCredentials(accessKeyID, secretAccessKey string) Option {
	return func(o *options) {
		o.creds = credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, "")
	}
}

// WithHTTPClient sends S3 and STS requests with c.
func WithHTTPClient(c *http.Client) Option {
	return func(o *options) { o.httpClient = c }
}

// New returns a Reader for the objects grants allow. Credentials and the
// default region come from the SDK's default chain (the ECS task role in
// the Fargate deployment) unless an option sets them. It fails without
// grants or for a grant whose bucket is not a valid S3 bucket name, and it
// does not contact AWS.
func New(ctx context.Context, grants []Grant, opts ...Option) (*Reader, error) {
	if len(grants) == 0 {
		return nil, errors.New("stateread: at least one grant is required")
	}
	for _, g := range grants {
		if !validBucket(g.Bucket) {
			return nil, fmt.Errorf("stateread: grant bucket %q is not a valid S3 bucket name", g.Bucket)
		}
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
		return nil, fmt.Errorf("stateread: load AWS configuration: %w", err)
	}
	if cfg.Region == "" {
		cfg.Region = defaultRegion
	}
	if !validRegion(cfg.Region) {
		return nil, fmt.Errorf("stateread: invalid region %q", cfg.Region)
	}
	if o.roleARN != "" {
		client := sts.NewFromConfig(cfg, func(so *sts.Options) {
			if o.endpoint != "" {
				so.BaseEndpoint = aws.String(o.endpoint)
			}
		})
		cfg.Credentials = aws.NewCredentialsCache(stscreds.NewAssumeRoleProvider(client, o.roleARN,
			func(ao *stscreds.AssumeRoleOptions) { ao.RoleSessionName = roleSessionName }))
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
	return &Reader{
		client: client,
		region: cfg.Region,
		grants: slices.Clone(grants),
		slots:  make(chan struct{}, MaxConcurrent),
	}, nil
}

// Read returns the outputs of the state object at loc. A location no grant
// allows fails with ErrNotAllowed before any request is made. The object is
// requested in loc.Region, or the Reader's region when it is empty, and
// once more in the region S3 names when it answers that the bucket is
// elsewhere. A missing object fails with ErrNotFound, refused credentials
// with ErrAccessDenied and other permanent S3 errors with ErrUnreadable;
// throttling, server errors and timeouts are returned wrapped as they are.
// Decoding errors are those of Decode. The body is read only as far as the
// outputs and is never kept.
func (r *Reader) Read(ctx context.Context, loc Location) (*State, error) {
	if !slices.ContainsFunc(r.grants, func(g Grant) bool { return g.Allows(loc) }) {
		return nil, fmt.Errorf("%w: %s", ErrNotAllowed, loc.url())
	}
	region := loc.Region
	if region == "" {
		region = r.region
	}
	if !validRegion(region) {
		return nil, fmt.Errorf("%w: %s: invalid region %q", ErrUnreadable, loc.url(), region)
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	select {
	case r.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, fmt.Errorf("stateread: wait to read %s: %w", loc.url(), ctx.Err())
	}
	defer func() { <-r.slots }()

	body, err := r.get(ctx, loc, region)
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()
	st, err := Decode(body)
	if err != nil {
		return nil, fmt.Errorf("stateread: %s: %w", loc.url(), err)
	}
	return st, nil
}

func (r *Reader) get(ctx context.Context, loc Location, region string) (io.ReadCloser, error) {
	out, err := r.getObject(ctx, loc, region)
	if moved := movedTo(err); moved != "" && moved != region {
		out, err = r.getObject(ctx, loc, moved)
	}
	if err != nil {
		return nil, classify(err, loc)
	}
	return out.Body, nil
}

func (r *Reader) getObject(ctx context.Context, loc Location, region string) (*s3.GetObjectOutput, error) {
	return r.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(loc.Bucket), Key: aws.String(loc.Key)},
		func(o *s3.Options) { o.Region = region })
}

func movedTo(err error) string {
	var resp *awshttp.ResponseError
	if !errors.As(err, &resp) || resp.HTTPStatusCode() != http.StatusMovedPermanently || resp.Response == nil {
		return ""
	}
	if region := resp.Response.Header.Get(bucketRegionField); validRegion(region) {
		return region
	}
	return ""
}

func classify(err error, loc Location) error {
	code, status := errorCode(err), statusCode(err)
	switch {
	case code == "NoSuchKey" || code == "NotFound" || status == http.StatusNotFound:
		return fmt.Errorf("%w: %s: %w", ErrNotFound, loc.url(), err)
	case code == "AccessDenied" || status == http.StatusForbidden:
		return fmt.Errorf("%w: %s: %w", ErrAccessDenied, loc.url(), err)
	case transient(code, status):
		return fmt.Errorf("stateread: get %s: %w", loc.url(), err)
	case status >= 300 && status < 500:
		return fmt.Errorf("%w: %s: %w", ErrUnreadable, loc.url(), err)
	}
	return fmt.Errorf("stateread: get %s: %w", loc.url(), err)
}

func transient(code string, status int) bool {
	_, throttle := retry.DefaultThrottleErrorCodes[code]
	_, retryable := retry.DefaultRetryableErrorCodes[code]
	return throttle || retryable || status == http.StatusRequestTimeout || status == http.StatusTooManyRequests
}

func errorCode(err error) string {
	var apiErr interface{ ErrorCode() string }
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode()
	}
	return ""
}

func statusCode(err error) int {
	var respErr interface{ HTTPStatusCode() int }
	if errors.As(err, &respErr) {
		return respErr.HTTPStatusCode()
	}
	return 0
}

func (l Location) url() string {
	return "s3://" + l.Bucket + "/" + l.Key
}
