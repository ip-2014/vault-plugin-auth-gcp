package gcpauth

import (
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SermoDigital/jose"
	"github.com/SermoDigital/jose/crypto"
	"github.com/SermoDigital/jose/jwt"
	"github.com/hashicorp/vault-plugin-auth-gcp/plugin/util"
	"github.com/hashicorp/vault/helper/policyutil"
	"github.com/hashicorp/vault/logical"
	"google.golang.org/api/iam/v1"
)

const (
	googleCredentialsEnv = "GOOGLE_CREDENTIALS"
)

func TestLoginIam(t *testing.T) {
	b, reqStorage := getTestBackend(t)

	creds, err := getTestCredentials()
	if err != nil {
		t.Fatal(err)
	}

	testConfigUpdate(t, b, reqStorage, map[string]interface{}{
		"credentials": os.Getenv(googleCredentialsEnv),
	})

	roleName := "testrole"
	testRoleCreate(t, b, reqStorage, map[string]interface{}{
		"name":             roleName,
		"type":             "iam",
		"policies":         "dev, prod",
		"project_id":       creds.ProjectId,
		"service_accounts": creds.ClientEmail,
		"ttl":              1800,
		"max_ttl":          1800,
	})

	// Have token expire within 5 minutes of max JWT exp
	expDelta := time.Duration(defaultMaxJwtExpMin-5) * time.Minute
	jwtVal := getTestIamToken(t, roleName, creds, expDelta)
	loginData := map[string]interface{}{
		"role": roleName,
		"jwt":  jwtVal,
	}

	metadata := map[string]string{
		"service_account_id":    creds.ClientId,
		"service_account_email": creds.ClientEmail,
		"role":                  roleName,
	}
	role := &gcpRole{
		RoleType:        "iam",
		ProjectId:       creds.ProjectId,
		Policies:        []string{"default", "dev", "prod"},
		TTL:             time.Duration(1800) * time.Second,
		MaxTTL:          time.Duration(1800) * time.Second,
		Period:          time.Duration(0),
		ServiceAccounts: []string{creds.ClientEmail},
	}
	testLoginIam(t, b, reqStorage, loginData, metadata, role, creds.ClientId)
}

func TestLoginIamWildcard(t *testing.T) {
	b, reqStorage := getTestBackend(t)

	creds, err := getTestCredentials()
	if err != nil {
		t.Fatal(err)
	}

	testConfigUpdate(t, b, reqStorage, map[string]interface{}{
		"credentials": os.Getenv(googleCredentialsEnv),
	})

	roleName := "testrole"
	testRoleCreate(t, b, reqStorage, map[string]interface{}{
		"name":             roleName,
		"type":             "iam",
		"project_id":       creds.ProjectId,
		"service_accounts": "*",
	})

	// Have token expire within 5 minutes of max JWT exp
	expDelta := time.Duration(defaultMaxJwtExpMin-5) * time.Minute
	jwtVal := getTestIamToken(t, roleName, creds, expDelta)
	loginData := map[string]interface{}{
		"role": roleName,
		"jwt":  jwtVal,
	}

	metadata := map[string]string{
		"service_account_id":    creds.ClientId,
		"service_account_email": creds.ClientEmail,
		"role":                  roleName,
	}
	role := &gcpRole{
		RoleType:        "iam",
		ProjectId:       creds.ProjectId,
		Policies:        []string{"default"},
		TTL:             time.Duration(0),
		MaxTTL:          time.Duration(0),
		Period:          time.Duration(0),
		ServiceAccounts: []string{creds.ClientEmail},
	}
	testLoginIam(t, b, reqStorage, loginData, metadata, role, creds.ClientId)
}

// TestLoginIam_UnauthorizedRole checks that we return an error response
// if the user attempts to login against a role it is not authorized for.
func TestLoginIam_UnauthorizedRole(t *testing.T) {
	b, reqStorage := getTestBackend(t)

	creds, err := getTestCredentials()
	if err != nil {
		t.Fatal(err)
	}

	roleName := "testrolenologin"

	testConfigUpdate(t, b, reqStorage, map[string]interface{}{
		"credentials": os.Getenv(googleCredentialsEnv),
	})
	testRoleCreate(t, b, reqStorage, map[string]interface{}{
		"type":             "iam",
		"name":             roleName,
		"project_id":       creds.ProjectId,
		"service_accounts": "notarealserviceaccount",
	})

	// Have token expire within 5 minutes of max JWT exp
	expDelta := time.Duration(defaultMaxJwtExpMin-5) * time.Minute
	jwtVal := getTestIamToken(t, roleName, creds, expDelta)
	loginData := map[string]interface{}{
		"role": roleName,
		"jwt":  jwtVal,
	}

	testLoginError(t, b, reqStorage, loginData, []string{
		"service account",
		creds.ClientEmail,
		creds.ClientId,
		"is not authorized for role",
	})
}

// TestLoginIam_MissingRole checks that we return an error response if role is not provided.
func TestLoginIam_MissingRole(t *testing.T) {
	b, reqStorage := getTestBackend(t)

	creds, err := getTestCredentials()
	if err != nil {
		t.Fatal(err)
	}

	roleName := "doesnotexist"

	testConfigUpdate(t, b, reqStorage, map[string]interface{}{
		"credentials": os.Getenv(googleCredentialsEnv),
	})

	// Have token expire within 5 minutes of max JWT exp
	expDelta := time.Duration(defaultMaxJwtExpMin-5) * time.Minute
	jwtVal := getTestIamToken(t, roleName, creds, expDelta)
	loginData := map[string]interface{}{
		"jwt": jwtVal,
	}
	testLoginError(t, b, reqStorage, loginData, []string{"role is required"})

	loginData["role"] = roleName
	testLoginError(t, b, reqStorage, loginData, []string{roleName, "not found"})
}

// TestLoginIam_ExpiredJwt checks that we return an error response for an expired JWT.
func TestLoginIam_ExpiredJwt(t *testing.T) {
	b, reqStorage := getTestBackend(t)

	creds, err := getTestCredentials()
	if err != nil {
		t.Fatal(err)
	}

	roleName := "testrole"
	testRoleCreate(t, b, reqStorage, map[string]interface{}{
		"name":             roleName,
		"type":             "iam",
		"policies":         "dev, prod",
		"project_id":       creds.ProjectId,
		"service_accounts": creds.ClientEmail,
	})

	// Create fake self-signed JWT to test.

	jwtVal := createExpiredIamToken(t, roleName, creds)
	loginData := map[string]interface{}{
		"role": roleName,
		"kid":  creds.PrivateKeyId,
		"jwt":  jwtVal,
	}

	testLoginError(t, b, reqStorage, loginData, []string{
		"invalid JWT",
		"token is expired",
	})
}

// TestLoginIam_JwtExpiresLate checks that we return an error response for an expired JWT.
func TestLoginIam_JwtExpiresTime(t *testing.T) {
	b, reqStorage := getTestBackend(t)

	creds, err := getTestCredentials()
	if err != nil {
		t.Fatal(err)
	}

	roleName := "testrole"

	maxJwtExpSeconds := 2400
	testRoleCreate(t, b, reqStorage, map[string]interface{}{
		"name":             roleName,
		"type":             "iam",
		"policies":         "dev, prod",
		"project_id":       creds.ProjectId,
		"service_accounts": creds.ClientEmail,
		"max_jwt_exp":      maxJwtExpSeconds,
	})

	badExpDelta := time.Duration(maxJwtExpSeconds+1200) * time.Second
	loginData := map[string]interface{}{
		"role": roleName,
		"jwt":  getTestIamToken(t, roleName, creds, badExpDelta),
	}

	testLoginError(t, b, reqStorage, loginData, []string{
		"invalid JWT",
		fmt.Sprintf("expire within %v", time.Duration(maxJwtExpSeconds)*time.Second),
	})

	validExpDelta := time.Duration(maxJwtExpSeconds-1200) * time.Second
	loginData["jwt"] = getTestIamToken(t, roleName, creds, validExpDelta)
	metadata := map[string]string{
		"service_account_id":    creds.ClientId,
		"service_account_email": creds.ClientEmail,
		"role":                  roleName,
	}
	role := &gcpRole{
		RoleType:        "iam",
		ProjectId:       creds.ProjectId,
		Policies:        []string{"default", "dev", "prod"},
		ServiceAccounts: []string{creds.ClientEmail},
	}
	testLoginIam(t, b, reqStorage, loginData, metadata, role, creds.ClientId)

}

func testLoginIam(
	t *testing.T, b logical.Backend, s logical.Storage,
	d map[string]interface{}, expectedMetadata map[string]string, role *gcpRole, personaName string) {
	resp, err := b.HandleRequest(&logical.Request{
		Operation: logical.UpdateOperation,
		Path:      "login",
		Data:      d,
		Storage:   s,
	})

	if err != nil {
		t.Fatal(err)
	}
	if resp != nil && resp.IsError() {
		t.Fatal(resp.Error())
	}

	if !policyutil.EquivalentPolicies(resp.Auth.Policies, role.Policies) {
		t.Fatalf("policy mismatch, expected %v but got %v", role.Policies, resp.Auth.Policies)
	}

	if resp.Auth.Period != role.Period {
		t.Fatalf("period mismatch, expected %v but got %v", role.Period, resp.Auth.Period)
	}

	// Check metadata
	for k, expected := range expectedMetadata {
		actual, ok := resp.Auth.Metadata[k]
		if !ok {
			t.Fatalf("metadata value '%s' not found, expected value '%s'", k, expected)
		}
		if actual != expected {
			t.Fatalf("metadata value '%s' mismatch, expected '%s' but got '%s'", k, expected, actual)
		}
	}

	if resp.Auth.Persona.Name != personaName {
		t.Fatalf("expected persona with name %s, got %s", personaName, resp.Auth.Persona.Name)
	}

	// Check lease options
	if !resp.Auth.LeaseOptions.Renewable {
		t.Fatal("expected lease options to be renewable")
	}
	if resp.Auth.LeaseOptions.TTL != role.TTL {
		t.Fatalf("lease option TTL mismatch, expected %v but got %v", role.TTL, resp.Auth.LeaseOptions.TTL)
	}
}

func testLoginError(t *testing.T, b logical.Backend, s logical.Storage, d map[string]interface{}, errorSubstrings []string) {
	resp, err := b.HandleRequest(&logical.Request{
		Operation: logical.UpdateOperation,
		Path:      "login",
		Data:      d,
		Storage:   s,
	})

	if err != nil {
		t.Fatal(err)
	}

	if !resp.IsError() {
		t.Fatal("expected error response")
	}

	errMsg := strings.ToLower(resp.Error().Error())
	for _, v := range errorSubstrings {
		if !strings.Contains(errMsg, strings.ToLower(v)) {
			t.Fatalf("expected '%s' to be in error: '%v'", v, resp.Error())
		}
	}
}

func getTestIamToken(t *testing.T, roleName string, creds *util.GcpCredentials, expDelta time.Duration) string {
	// Generate signed JWT to login with.
	httpClient, err := util.GetHttpClient(creds, iam.CloudPlatformScope)
	if err != nil {
		t.Fatal(err)
	}
	iamClient, err := iam.New(httpClient)
	if err != nil {
		t.Fatal(err)
	}

	expectedJwtAud := fmt.Sprintf(expectedJwtAudTemplate, roleName)
	exp := time.Now().Add(expDelta)
	signedJwtResp, err := util.ServiceAccountLoginJwt(iamClient, exp, expectedJwtAud, creds.ProjectId, creds.ClientEmail)
	if err != nil {
		t.Fatal(err)
	}

	return signedJwtResp.SignedJwt
}

func createExpiredIamToken(t *testing.T, roleName string, creds *util.GcpCredentials) string {
	// Create header.
	header, err := jose.Protected{
		"alg": crypto.SigningMethodRS256.Alg(),
		"kid": creds.PrivateKeyId,
		"typ": "JWT",
	}.Base64()
	if err != nil {
		t.Fatal(err)
	}

	claims := jwt.Claims{}
	claims.SetAudience(fmt.Sprintf(expectedJwtAudTemplate, roleName))
	claims.SetSubject(creds.ClientId)
	claims.SetExpiration(time.Now().Add(-100 * time.Minute))
	claimBytes, err := claims.Base64()
	if err != nil {
		t.Fatal(err)
	}

	// Create JWT signature.
	toSign := fmt.Sprintf("%s.%s", string(header), string(claimBytes))
	h := sha256.New()
	h.Write([]byte(toSign))

	key, err := crypto.ParseRSAPrivateKeyFromPEM([]byte(creds.PrivateKey))
	if err != nil {
		t.Fatal(err)
	}
	sig, err := crypto.SigningMethodRS256.Sign([]byte(toSign), key)
	if err != nil {
		t.Fatal(err)
	}
	sig64, err := sig.Base64()
	if err != nil {
		t.Fatal(err)
	}

	return fmt.Sprintf("%s.%s", toSign, sig64)
}
