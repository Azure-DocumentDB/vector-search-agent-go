package safety

import (
	"context"
	"errors"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"math"
	"strings"
	"testing"
	"time"
)

type tokenStub struct {
	token azcore.AccessToken
	scope string
	err   error
}

func (s *tokenStub) GetToken(_ context.Context, p policy.TokenRequestOptions) (azcore.AccessToken, error) {
	s.scope = strings.Join(p.Scopes, ",")
	return s.token, s.err
}
func TestEndpointAndNativeURIGuards(t *testing.T) {
	for _, raw := range []string{"http://unit.openai.azure.com/", "https://unit.openai.azure.com.evil.test/", "https://user@unit.openai.azure.com/", "https://unit.openai.azure.com:443/", "https://unit.openai.azure.com/openai/v1", "https://unit.openai.azure.com/?x=1", "https://unit.openai.azure.com/#x", "https://127.0.0.1/", "https://unit.openai.azure.com/%2e%2e"} {
		if ModelEndpoint(raw) == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	if ModelEndpoint("https://unit.openai.azure.com/") != nil {
		t.Fatal("valid model endpoint rejected")
	}
	host := "unit.global.mongocluster.cosmos.azure.com"
	base := "mongodb+srv://user:synthetic@" + host + "/"
	if NativeURI(base+"?tls=true&retryWrites=false&authMechanism=SCRAM-SHA-256", host) != nil {
		t.Fatal("valid URI rejected")
	}
	for _, q := range []string{"tls=false", "tlsInsecure=true", "tlsAllowInvalidCertificates=true", "tlsCAFile=x", "srvServiceName=evil", "authMechanism=MONGODB-OIDC", "authSource=foreign", "retryWrites=true", "proxyHost=evil", "tls=true&TLS=true", "appName=x&appName=y", "unknown=x"} {
		if NativeURI(base+"?"+q, host) == nil {
			t.Errorf("accepted %s", q)
		}
	}
	for _, raw := range []string{strings.Replace(base, host, "evil.test", 1), strings.Replace(base, "mongodb+srv", "mongodb", 1), base + "Hotels", strings.Replace(base, "user:synthetic@", "", 1)} {
		if NativeURI(raw, host) == nil {
			t.Error("unsafe URI accepted")
		}
	}
}
func TestTokensFixedAudienceAndExpiry(t *testing.T) {
	now := time.Now()
	stub := &tokenStub{token: azcore.AccessToken{Token: "synthetic-token", ExpiresOn: now.Add(time.Hour)}}
	for _, scope := range []string{MongoScope, ModelScope} {
		c := CheckedCredential{Inner: stub, Scope: scope, Now: func() time.Time { return now }}
		token, err := c.GetToken(context.Background(), policy.TokenRequestOptions{Scopes: []string{"https://evil.test/.default"}})
		if err != nil || stub.scope != scope || token.ExpiresOn != stub.token.ExpiresOn {
			t.Fatal("audience/expiry not preserved")
		}
	}
	for _, token := range []azcore.AccessToken{{Token: "", ExpiresOn: now.Add(time.Hour)}, {Token: "x", ExpiresOn: now}, {Token: "x", ExpiresOn: now.Add(30 * time.Second)}, {Token: "x\r\ny", ExpiresOn: now.Add(time.Hour)}} {
		stub.token = token
		if _, err := (CheckedCredential{Inner: stub, Scope: MongoScope}).GetToken(context.Background(), policy.TokenRequestOptions{}); err == nil {
			t.Fatal("invalid token accepted")
		}
	}
	stub.err = errors.New("synthetic-sensitive-body")
	_, err := (CheckedCredential{Inner: stub, Scope: ModelScope}).GetToken(context.Background(), policy.TokenRequestOptions{})
	if strings.Contains(err.Error(), "synthetic-sensitive") {
		t.Fatal("leaked error")
	}
}
func TestVectorQueryAndSafeError(t *testing.T) {
	for _, v := range [][]float32{nil, {0, 0}, {1}, {float32(math.Inf(1)), 1}, {float32(math.NaN()), 1}} {
		if Vector(v, 2) == nil {
			t.Fatal("bad vector accepted")
		}
	}
	if Vector([]float32{1, 0}, 2) != nil {
		t.Fatal("valid vector rejected")
	}
	for _, k := range []int{-1, 0, 21} {
		if Query("hotel", k) == nil {
			t.Fatal("bad k accepted")
		}
	}
	for _, q := range []string{"", " ", strings.Repeat("x", 2049), "a\x00b"} {
		if Query(q, 1) == nil {
			t.Fatal("bad query accepted")
		}
	}
	cause := context.Canceled
	err := Protect("test", cause)
	if !errors.Is(err, cause) || strings.Contains(err.Error(), cause.Error()) {
		t.Fatal("safe error contract")
	}
}
