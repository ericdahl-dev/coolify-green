package coolify

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Kind is the type of a monitored Coolify resource.
type Kind string

const (
	KindApplication Kind = "application"
	KindService     Kind = "service"
	KindDatabase    Kind = "database"
)

// Label returns a short human label for the kind.
func (k Kind) Label() string {
	switch k {
	case KindApplication:
		return "app"
	case KindService:
		return "service"
	case KindDatabase:
		return "database"
	default:
		return string(k)
	}
}

// Environment is a named environment inside a Project (usually "production").
type Environment struct {
	ID   int    `json:"id"`
	UUID string `json:"uuid"`
	Name string `json:"name"`
}

// Project is a Coolify project — the grouping the dashboard displays as a row.
type Project struct {
	UUID         string        `json:"uuid"`
	Name         string        `json:"name"`
	Description  string        `json:"description"`
	Environments []Environment `json:"environments"`
}

// Server is a machine Coolify deploys onto.
type Server struct {
	UUID string `json:"uuid"`
	Name string `json:"name"`
	IP   string `json:"ip"`
}

// Resource is an application, service, or database. Coolify returns these from
// three separate endpoints with overlapping shapes, so one struct decodes all
// three and Kind records which endpoint it came from.
type Resource struct {
	Kind          Kind   `json:"-"`
	UUID          string `json:"uuid"`
	Name          string `json:"name"`
	Status        string `json:"status"`
	Description   string `json:"description"`
	EnvironmentID int    `json:"environment_id"`
	FQDN          string `json:"fqdn"`
	GitRepository string `json:"git_repository"`
	GitBranch     string `json:"git_branch"`
	ServerStatus  bool   `json:"server_status"`
	UpdatedAt     string `json:"updated_at"`
	LastOnlineAt  string `json:"last_online_at"`

	// Applications carry their server under destination; services and
	// databases repeat it at the top level.
	Destination struct {
		Server Server `json:"server"`
	} `json:"destination"`
	Server Server `json:"server"`
}

// ServerName returns the name of the server this resource runs on, from
// whichever of the two places the API put it.
func (r Resource) ServerName() string {
	if r.Destination.Server.Name != "" {
		return r.Destination.Server.Name
	}
	return r.Server.Name
}

// Deployment is one deploy of an application.
type Deployment struct {
	UUID            string     `json:"deployment_uuid"`
	ApplicationID   string     `json:"application_id"`
	ApplicationName string     `json:"application_name"`
	Status          string     `json:"status"`
	Commit          string     `json:"commit"`
	CommitMessage   string     `json:"commit_message"`
	ServerName      string     `json:"server_name"`
	DeploymentURL   string     `json:"deployment_url"`
	IsAPI           bool       `json:"is_api"`
	IsWebhook       bool       `json:"is_webhook"`
	ForceRebuild    bool       `json:"force_rebuild"`
	Rollback        bool       `json:"rollback"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	FinishedAt      *time.Time `json:"finished_at"`
	Logs            string     `json:"logs"`
}

// ShortCommit returns the first seven characters of the commit sha.
func (d Deployment) ShortCommit() string {
	if len(d.Commit) > 7 {
		return d.Commit[:7]
	}
	return d.Commit
}

// CommitSubject returns the first line of the commit message.
func (d Deployment) CommitSubject() string {
	line, _, _ := strings.Cut(d.CommitMessage, "\n")
	return strings.TrimSpace(line)
}

// Trigger describes what started the deployment, for display.
func (d Deployment) Trigger() string {
	switch {
	case d.Rollback:
		return "rollback"
	case d.IsWebhook:
		return "webhook"
	case d.IsAPI:
		return "api"
	default:
		return "manual"
	}
}

// deploymentPage is the envelope returned by the per-application deployments
// endpoint, which paginates while the instance-wide one returns a bare array.
// deploymentList decodes Coolify's deployment list, which arrives as an array
// normally but as an object keyed by index when the server filtered out some
// entries: Laravel serializes a collection with gaps in its keys that way.
type deploymentList []Deployment

func (l *deploymentList) UnmarshalJSON(data []byte) error {
	var list []Deployment
	if err := json.Unmarshal(data, &list); err == nil {
		*l = list
		return nil
	}
	var keyed map[string]Deployment
	if err := json.Unmarshal(data, &keyed); err != nil {
		return err
	}
	keys := make([]string, 0, len(keyed))
	for k := range keyed {
		keys = append(keys, k)
	}
	// Keep the server's order: keys are indexes, so compare them as numbers.
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) != len(keys[j]) {
			return len(keys[i]) < len(keys[j])
		}
		return keys[i] < keys[j]
	})
	list = make([]Deployment, 0, len(keys))
	for _, k := range keys {
		list = append(list, keyed[k])
	}
	*l = list
	return nil
}

type deploymentPage struct {
	Count       int            `json:"count"`
	Deployments deploymentList `json:"deployments"`
}

// LogLine is one entry from a deployment's log stream. Coolify stores these as
// a JSON array encoded inside the deployment's "logs" string field.
type LogLine struct {
	Command   string `json:"command"`
	Output    string `json:"output"`
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Hidden    bool   `json:"hidden"`
	Batch     int    `json:"batch"`
	Order     int    `json:"order"`
}

// IsError reports whether the line came from stderr.
func (l LogLine) IsError() bool { return l.Type == "stderr" }

// ParseLogs decodes the doubly-encoded log payload on a Deployment. An empty
// or malformed payload yields no lines rather than an error, because a partly
// written log on an in-flight deploy is normal.
func ParseLogs(raw string) []LogLine {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var lines []LogLine
	if err := json.Unmarshal([]byte(raw), &lines); err != nil {
		return nil
	}
	return lines
}

// VisibleLogs drops the lines Coolify marks hidden (internal helper-container
// plumbing) so the dashboard shows only build output a human cares about.
func VisibleLogs(lines []LogLine) []LogLine {
	var out []LogLine
	for _, l := range lines {
		if l.Hidden {
			continue
		}
		out = append(out, l)
	}
	return out
}

// ParseLooseTime parses the two timestamp shapes Coolify emits: RFC 3339 for
// most fields and a space-separated form for last_online_at.
func ParseLooseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// applicationUUIDPattern extracts the application uuid from a deployment_url.
// The instance-wide deployments endpoint identifies the application only by
// its numeric id and its (non-unique) name, so the url is the sole reliable
// link back to the application uuid.
var applicationUUIDPattern = regexp.MustCompile(`/application/([^/]+)/deployment/`)

// ApplicationUUID returns the uuid of the application this deployment belongs
// to, or "" when the deployment_url is missing or in an unexpected shape.
func (d Deployment) ApplicationUUID() string {
	m := applicationUUIDPattern.FindStringSubmatch(d.DeploymentURL)
	if len(m) != 2 {
		return ""
	}
	return m[1]
}
