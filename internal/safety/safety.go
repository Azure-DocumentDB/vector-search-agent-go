// Package safety contains fail-closed destination, credential, and input guards.
package safety

import (
	"context"
	"errors"
	"math"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

const MongoScope = "https://ossrdbms-aad.database.windows.net/.default"
const ModelScope = "https://cognitiveservices.azure.com/.default"

var mongoHost = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.(?:global\.)?mongocluster\.cosmos\.azure\.com$`)
var modelHost = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.openai\.azure\.com$`)
var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

// Safe errors intentionally hide SDK bodies, URIs, token values, and user data.
type Error struct {
	Operation string
	Cause     error
}

func (e Error) Error() string                   { return e.Operation + " failed (service details withheld)" }
func (e Error) Unwrap() error                   { return e.Cause }
func Protect(operation string, err error) error { return Error{operation, err} }
func Identifier(s string) bool                  { return identifier.MatchString(s) }
func MongoHost(host string) bool                { return mongoHost.MatchString(host) }

func ModelEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !modelHost.MatchString(u.Hostname()) || u.Host != u.Hostname() || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return errors.New("AZURE_OPENAI_ENDPOINT must be exactly https://<resource>.openai.azure.com/ (public Azure, no credentials, port, path, or query)")
	}
	return nil
}

// NativeURI rejects unknown/duplicate options before SRV lookup or any credential construction.
func NativeURI(raw, host string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "mongodb+srv" || !MongoHost(host) || u.Host != host || u.User == nil || u.User.Username() == "" || (u.Path != "" && u.Path != "/") || u.RawPath != "" || u.Fragment != "" {
		return errors.New("native auth requires an explicit Azure mongodb+srv URI matching AZURE_DOCUMENTDB_HOST, without a database path")
	}
	p, ok := u.User.Password()
	if !ok || p == "" {
		return errors.New("native auth requires username and password")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return errors.New("invalid native URI options")
	}
	seen := map[string]bool{}
	for k, values := range q {
		k = strings.ToLower(k)
		if seen[k] || len(values) != 1 {
			return errors.New("duplicate native URI options")
		}
		seen[k] = true
		v := values[0]
		switch k {
		case "tls", "ssl":
			if v != "true" {
				return errors.New("TLS must be enabled")
			}
		case "retrywrites":
			if v != "false" {
				return errors.New("retryWrites must be false")
			}
		case "authsource":
			if v != "admin" {
				return errors.New("native authSource must be admin")
			}
		case "authmechanism":
			if v != "SCRAM-SHA-256" {
				return errors.New("native authMechanism must be SCRAM-SHA-256")
			}
		case "appname":
			if !Identifier(v) {
				return errors.New("invalid appName")
			}
		default:
			return errors.New("unsupported native URI option (TLS overrides, custom SRV, and credential mechanism properties are forbidden)")
		}
	}
	return nil
}

// CheckedCredential fixes the audience and rejects empty, expired or near-expiry tokens.
// Azure Identity authenticates token provenance; this is not an unverified JWT parser.
type CheckedCredential struct {
	Inner azcore.TokenCredential
	Scope string
	Now   func() time.Time
}

func (c CheckedCredential) GetToken(ctx context.Context, _ policy.TokenRequestOptions) (azcore.AccessToken, error) {
	if c.Inner == nil || (c.Scope != MongoScope && c.Scope != ModelScope) {
		return azcore.AccessToken{}, errors.New("invalid credential configuration")
	}
	token, err := c.Inner.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{c.Scope}})
	if err != nil {
		return azcore.AccessToken{}, Protect("Azure token acquisition", err)
	}
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	if strings.TrimSpace(token.Token) == "" || strings.ContainsAny(token.Token, "\r\n") || !token.ExpiresOn.After(now.Add(time.Minute)) {
		return azcore.AccessToken{}, errors.New("Azure token is empty, expired, or near expiry")
	}
	return token, nil
}

func Vector(v []float32, dimensions int) error {
	if dimensions < 1 || dimensions > 2000 || len(v) != dimensions {
		return errors.New("embedding dimensions do not match configuration")
	}
	nonzero := false
	for _, n := range v {
		if math.IsNaN(float64(n)) || math.IsInf(float64(n), 0) {
			return errors.New("embedding contains a non-finite number")
		}
		nonzero = nonzero || n != 0
	}
	if !nonzero {
		return errors.New("embedding must not be all zero")
	}
	return nil
}

func Query(query string, k int) error {
	if len(strings.TrimSpace(query)) == 0 || len(query) > 2048 || strings.ContainsRune(query, '\x00') {
		return errors.New("query must be nonempty text of at most 2048 bytes without NUL")
	}
	if k < 1 || k > 20 {
		return errors.New("nearestNeighbors must be an integer in [1,20]")
	}
	return nil
}
