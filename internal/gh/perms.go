package gh

import (
	"context"
	"errors"
)

// Collaborator permission levels returned by CollaboratorPermission.
const (
	PermissionAdmin = "admin"
	PermissionWrite = "write"
	PermissionRead  = "read"
	PermissionNone  = "none"
)

// Membership states returned by TeamMembership and UserOrgMembership.
const (
	MembershipActive  = "active"
	MembershipPending = "pending"
	MembershipNone    = "none"
)

func isNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

// CollaboratorPermission returns a user's permission on a repository:
// admin, write, read or none. Maintain maps to write and triage to read, as
// GitHub's legacy permission field does; an unknown user is none.
func (c *Client) CollaboratorPermission(ctx context.Context, repo, login string) (string, error) {
	rp, err := repoPath(repo)
	if err != nil {
		return "", err
	}
	user, err := segment("login", login)
	if err != nil {
		return "", err
	}
	var out struct {
		Permission string `json:"permission"`
		RoleName   string `json:"role_name"`
	}
	err = c.get(ctx, "/repos/{owner}/{repo}/collaborators/{username}/permission", rp+"/collaborators/"+user+"/permission", &out)
	if isNotFound(err) {
		return PermissionNone, nil
	}
	if err != nil {
		return "", err
	}
	switch out.Permission {
	case PermissionAdmin, PermissionWrite, PermissionRead:
		return out.Permission, nil
	case "maintain":
		return PermissionWrite, nil
	case "triage":
		return PermissionRead, nil
	}
	return PermissionNone, nil
}

// HasPushPermission reports whether a permission from
// CollaboratorPermission allows pushing.
func HasPushPermission(perm string) bool {
	switch perm {
	case PermissionAdmin, PermissionWrite, "maintain":
		return true
	}
	return false
}

// TeamMembership returns a user's membership state in a team: active,
// pending or none. Members of child teams are members of the parent, as
// GitHub reports them.
func (c *Client) TeamMembership(ctx context.Context, org, teamSlug, login string) (string, error) {
	o, err := segment("org", org)
	if err != nil {
		return "", err
	}
	team, err := segment("team", teamSlug)
	if err != nil {
		return "", err
	}
	user, err := segment("login", login)
	if err != nil {
		return "", err
	}
	var out struct {
		State string `json:"state"`
	}
	err = c.get(ctx, "/orgs/{org}/teams/{team_slug}/memberships/{username}", "/orgs/"+o+"/teams/"+team+"/memberships/"+user, &out)
	if isNotFound(err) {
		return MembershipNone, nil
	}
	if err != nil {
		return "", err
	}
	if out.State == "" {
		return MembershipNone, nil
	}
	return out.State, nil
}

// AuthenticatedUser returns the user a token client acts for.
func (c *Client) AuthenticatedUser(ctx context.Context) (*User, error) {
	var u User
	if err := c.get(ctx, "/user", "/user", &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// UserOrgs returns the logins of the organisations the authenticated user
// belongs to and has granted the App access to.
func (c *Client) UserOrgs(ctx context.Context) ([]string, error) {
	orgs, err := list[User](ctx, c, "/user/orgs", "/user/orgs", nil, "", 0)
	if err != nil {
		return nil, err
	}
	logins := make([]string, len(orgs))
	for i, o := range orgs {
		logins[i] = o.Login
	}
	return logins, nil
}

// UserOrgMembership returns the authenticated user's membership in an
// organisation: state active, pending or none, and role admin or member.
func (c *Client) UserOrgMembership(ctx context.Context, org string) (state, role string, err error) {
	o, err := segment("org", org)
	if err != nil {
		return "", "", err
	}
	var out struct {
		State string `json:"state"`
		Role  string `json:"role"`
	}
	err = c.get(ctx, "/user/memberships/orgs/{org}", "/user/memberships/orgs/"+o, &out)
	if isNotFound(err) {
		return MembershipNone, "", nil
	}
	if err != nil {
		return "", "", err
	}
	return out.State, out.Role, nil
}
