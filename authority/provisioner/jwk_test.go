package provisioner

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pkg/errors"
	"github.com/smallstep/assert"
	"github.com/smallstep/certificates/errs"
	"github.com/smallstep/cli/jose"
)

func TestJWK_Getters(t *testing.T) {
	p, err := generateJWK()
	assert.FatalError(t, err)
	if got := p.GetID(); got != p.Name+":"+p.Key.KeyID {
		t.Errorf("JWK.GetID() = %v, want %v:%v", got, p.Name, p.Key.KeyID)
	}
	if got := p.GetName(); got != p.Name {
		t.Errorf("JWK.GetName() = %v, want %v", got, p.Name)
	}
	if got := p.GetType(); got != TypeJWK {
		t.Errorf("JWK.GetType() = %v, want %v", got, TypeJWK)
	}
	kid, key, ok := p.GetEncryptedKey()
	if kid != p.Key.KeyID || key != p.EncryptedKey || ok == false {
		t.Errorf("JWK.GetEncryptedKey() = (%v, %v, %v), want (%v, %v, %v)",
			kid, key, ok, p.Key.KeyID, p.EncryptedKey, true)
	}
	p.EncryptedKey = ""
	kid, key, ok = p.GetEncryptedKey()
	if kid != p.Key.KeyID || key != "" || ok == true {
		t.Errorf("JWK.GetEncryptedKey() = (%v, %v, %v), want (%v, %v, %v)",
			kid, key, ok, p.Key.KeyID, "", false)
	}
}

func TestJWK_Init(t *testing.T) {
	type ProvisionerValidateTest struct {
		p   *JWK
		err error
	}
	tests := map[string]func(*testing.T) ProvisionerValidateTest{
		"fail-empty": func(t *testing.T) ProvisionerValidateTest {
			return ProvisionerValidateTest{
				p:   &JWK{},
				err: errors.New("provisioner type cannot be empty"),
			}
		},
		"fail-empty-name": func(t *testing.T) ProvisionerValidateTest {
			return ProvisionerValidateTest{
				p: &JWK{
					Type: "JWK",
				},
				err: errors.New("provisioner name cannot be empty"),
			}
		},
		"fail-empty-type": func(t *testing.T) ProvisionerValidateTest {
			return ProvisionerValidateTest{
				p:   &JWK{Name: "foo"},
				err: errors.New("provisioner type cannot be empty"),
			}
		},
		"fail-empty-key": func(t *testing.T) ProvisionerValidateTest {
			return ProvisionerValidateTest{
				p:   &JWK{Name: "foo", Type: "bar"},
				err: errors.New("provisioner key cannot be empty"),
			}
		},
		"fail-bad-claims": func(t *testing.T) ProvisionerValidateTest {
			return ProvisionerValidateTest{
				p:   &JWK{Name: "foo", Type: "bar", Key: &jose.JSONWebKey{}, audiences: testAudiences, Claims: &Claims{DefaultTLSDur: &Duration{0}}},
				err: errors.New("claims: DefaultTLSCertDuration must be greater than 0"),
			}
		},
		"ok": func(t *testing.T) ProvisionerValidateTest {
			return ProvisionerValidateTest{
				p: &JWK{Name: "foo", Type: "bar", Key: &jose.JSONWebKey{}, audiences: testAudiences},
			}
		},
	}

	config := Config{
		Claims:    globalProvisionerClaims,
		Audiences: testAudiences,
	}
	for name, get := range tests {
		t.Run(name, func(t *testing.T) {
			tc := get(t)
			err := tc.p.Init(config)
			if err != nil {
				if assert.NotNil(t, tc.err) {
					assert.Equals(t, tc.err.Error(), err.Error())
				}
			} else {
				assert.Nil(t, tc.err)
			}
		})
	}
}

func TestJWK_authorizeToken(t *testing.T) {
	p1, err := generateJWK()
	assert.FatalError(t, err)
	p2, err := generateJWK()
	assert.FatalError(t, err)

	key1, err := decryptJSONWebKey(p1.EncryptedKey)
	assert.FatalError(t, err)
	key2, err := decryptJSONWebKey(p2.EncryptedKey)
	assert.FatalError(t, err)

	t1, err := generateSimpleToken(p1.Name, testAudiences.Sign[0], key1)
	assert.FatalError(t, err)
	t2, err := generateSimpleToken(p2.Name, testAudiences.Sign[1], key2)
	assert.FatalError(t, err)
	t3, err := generateToken("test.smallstep.com", p1.Name, testAudiences.Sign[0], "", []string{}, time.Now(), key1)
	assert.FatalError(t, err)

	// Invalid tokens
	parts := strings.Split(t1, ".")
	key3, err := generateJSONWebKey()
	assert.FatalError(t, err)
	// missing key
	failKey, err := generateSimpleToken(p1.Name, testAudiences.Sign[0], key3)
	assert.FatalError(t, err)
	// invalid token
	failTok := "foo." + parts[1] + "." + parts[2]
	// invalid claims
	failClaims := parts[0] + ".foo." + parts[1]
	// invalid issuer
	failIss, err := generateSimpleToken("foobar", testAudiences.Sign[0], key1)
	assert.FatalError(t, err)
	// invalid audience
	failAud, err := generateSimpleToken(p1.Name, "foobar", key1)
	assert.FatalError(t, err)
	// invalid signature
	failSig := t1[0 : len(t1)-2]
	// no subject
	failSub, err := generateToken("", p1.Name, testAudiences.Sign[0], "", []string{"test.smallstep.com"}, time.Now(), key1)
	assert.FatalError(t, err)
	// expired
	failExp, err := generateToken("subject", p1.Name, testAudiences.Sign[0], "", []string{"test.smallstep.com"}, time.Now().Add(-360*time.Second), key1)
	assert.FatalError(t, err)
	// not before
	failNbf, err := generateToken("subject", p1.Name, testAudiences.Sign[0], "", []string{"test.smallstep.com"}, time.Now().Add(360*time.Second), key1)
	assert.FatalError(t, err)

	// Remove encrypted key for p2
	p2.EncryptedKey = ""

	type args struct {
		token string
	}
	tests := []struct {
		name string
		prov *JWK
		args args
		code int
		err  error
	}{
		{"fail-token", p1, args{failTok}, http.StatusUnauthorized, errors.New("jwk.authorizeToken; error parsing jwk token")},
		{"fail-key", p1, args{failKey}, http.StatusUnauthorized, errors.New("jwk.authorizeToken; error parsing jwk claims")},
		{"fail-claims", p1, args{failClaims}, http.StatusUnauthorized, errors.New("jwk.authorizeToken; error parsing jwk claims")},
		{"fail-signature", p1, args{failSig}, http.StatusUnauthorized, errors.New("jwk.authorizeToken; error parsing jwk claims: square/go-jose: error in cryptographic primitive")},
		{"fail-issuer", p1, args{failIss}, http.StatusUnauthorized, errors.New("jwk.authorizeToken; invalid jwk claims: square/go-jose/jwt: validation failed, invalid issuer claim (iss)")},
		{"fail-expired", p1, args{failExp}, http.StatusUnauthorized, errors.New("jwk.authorizeToken; invalid jwk claims: square/go-jose/jwt: validation failed, token is expired (exp)")},
		{"fail-not-before", p1, args{failNbf}, http.StatusUnauthorized, errors.New("jwk.authorizeToken; invalid jwk claims: square/go-jose/jwt: validation failed, token not valid yet (nbf)")},
		{"fail-audience", p1, args{failAud}, http.StatusUnauthorized, errors.New("jwk.authorizeToken; invalid jwk token audience claim (aud)")},
		{"fail-subject", p1, args{failSub}, http.StatusUnauthorized, errors.New("jwk.authorizeToken; jwk token subject cannot be empty")},
		{"ok", p1, args{t1}, http.StatusOK, nil},
		{"ok-no-encrypted-key", p2, args{t2}, http.StatusOK, nil},
		{"ok-no-sans", p1, args{t3}, http.StatusOK, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := tt.prov.authorizeToken(tt.args.token, testAudiences.Sign); err != nil {
				if assert.NotNil(t, tt.err) {
					sc, ok := err.(errs.StatusCoder)
					assert.Fatal(t, ok, "error does not implement StatusCoder interface")
					assert.Equals(t, sc.StatusCode(), tt.code)
					assert.HasPrefix(t, err.Error(), tt.err.Error())
				}
			} else {
				assert.Nil(t, tt.err)
				assert.NotNil(t, got)
			}
		})
	}
}

func TestJWK_AuthorizeRevoke(t *testing.T) {
	p1, err := generateJWK()
	assert.FatalError(t, err)
	key1, err := decryptJSONWebKey(p1.EncryptedKey)
	assert.FatalError(t, err)
	t1, err := generateSimpleToken(p1.Name, testAudiences.Revoke[0], key1)
	assert.FatalError(t, err)
	// invalid signature
	failSig := t1[0 : len(t1)-2]

	type args struct {
		token string
	}
	tests := []struct {
		name string
		prov *JWK
		args args
		code int
		err  error
	}{
		{"fail-signature", p1, args{failSig}, http.StatusUnauthorized, errors.New("jwk.AuthorizeRevoke: jwk.authorizeToken; error parsing jwk claims: square/go-jose: error in cryptographic primitive")},
		{"ok", p1, args{t1}, http.StatusOK, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.prov.AuthorizeRevoke(context.Background(), tt.args.token); err != nil {
				if assert.NotNil(t, tt.err) {
					sc, ok := err.(errs.StatusCoder)
					assert.Fatal(t, ok, "error does not implement StatusCoder interface")
					assert.Equals(t, sc.StatusCode(), tt.code)
					assert.HasPrefix(t, err.Error(), tt.err.Error())
				}
			}
		})
	}
}

func TestJWK_AuthorizeSign(t *testing.T) {
	p1, err := generateJWK()
	assert.FatalError(t, err)
	key1, err := decryptJSONWebKey(p1.EncryptedKey)
	assert.FatalError(t, err)

	t1, err := generateToken("subject", p1.Name, testAudiences.Sign[0], "name@smallstep.com", []string{"127.0.0.1", "max@smallstep.com", "foo"}, time.Now(), key1)
	assert.FatalError(t, err)

	t2, err := generateToken("subject", p1.Name, testAudiences.Sign[0], "name@smallstep.com", []string{}, time.Now(), key1)
	assert.FatalError(t, err)

	// invalid signature
	failSig := t1[0 : len(t1)-2]

	type args struct {
		token string
	}
	tests := []struct {
		name   string
		prov   *JWK
		args   args
		code   int
		err    error
		dns    []string
		emails []string
		ips    []net.IP
	}{
		{name: "fail-signature", prov: p1, args: args{failSig}, code: http.StatusUnauthorized, err: errors.New("jwk.AuthorizeSign: jwk.authorizeToken; error parsing jwk claims: square/go-jose: error in cryptographic primitive")},
		{"ok-sans", p1, args{t1}, http.StatusOK, nil, []string{"foo"}, []string{"max@smallstep.com"}, []net.IP{net.ParseIP("127.0.0.1")}},
		{"ok-no-sans", p1, args{t2}, http.StatusOK, nil, []string{"subject"}, []string{}, []net.IP{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := NewContextWithMethod(context.Background(), SignMethod)
			if got, err := tt.prov.AuthorizeSign(ctx, tt.args.token); err != nil {
				if assert.NotNil(t, tt.err) {
					sc, ok := err.(errs.StatusCoder)
					assert.Fatal(t, ok, "error does not implement StatusCoder interface")
					assert.Equals(t, sc.StatusCode(), tt.code)
					assert.HasPrefix(t, err.Error(), tt.err.Error())
				}
			} else {
				if assert.NotNil(t, got) {
					assert.Len(t, 8, got)
					for _, o := range got {
						switch v := o.(type) {
						case *provisionerExtensionOption:
							assert.Equals(t, v.Type, int(TypeJWK))
							assert.Equals(t, v.Name, tt.prov.GetName())
							assert.Equals(t, v.CredentialID, tt.prov.Key.KeyID)
							assert.Len(t, 0, v.KeyValuePairs)
						case profileDefaultDuration:
							assert.Equals(t, time.Duration(v), tt.prov.claimer.DefaultTLSCertDuration())
						case commonNameValidator:
							assert.Equals(t, string(v), "subject")
						case defaultPublicKeyValidator:
						case dnsNamesValidator:
							assert.Equals(t, []string(v), tt.dns)
						case emailAddressesValidator:
							assert.Equals(t, []string(v), tt.emails)
						case ipAddressesValidator:
							assert.Equals(t, []net.IP(v), tt.ips)
						case *validityValidator:
							assert.Equals(t, v.min, tt.prov.claimer.MinTLSCertDuration())
							assert.Equals(t, v.max, tt.prov.claimer.MaxTLSCertDuration())
						default:
							assert.FatalError(t, errors.Errorf("unexpected sign option of type %T", v))
						}
					}
				}
			}
		})
	}
}

func TestJWK_AuthorizeRenew(t *testing.T) {
	p1, err := generateJWK()
	assert.FatalError(t, err)
	p2, err := generateJWK()
	assert.FatalError(t, err)

	// disable renewal
	disable := true
	p2.Claims = &Claims{DisableRenewal: &disable}
	p2.claimer, err = NewClaimer(p2.Claims, globalProvisionerClaims)
	assert.FatalError(t, err)

	type args struct {
		cert *x509.Certificate
	}
	tests := []struct {
		name    string
		prov    *JWK
		args    args
		code    int
		wantErr bool
	}{
		{"ok", p1, args{nil}, http.StatusOK, false},
		{"fail/renew-disabled", p2, args{nil}, http.StatusUnauthorized, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.prov.AuthorizeRenew(context.Background(), tt.args.cert); (err != nil) != tt.wantErr {
				t.Errorf("JWK.AuthorizeRenew() error = %v, wantErr %v", err, tt.wantErr)
			} else if err != nil {
				sc, ok := err.(errs.StatusCoder)
				assert.Fatal(t, ok, "error does not implement StatusCoder interface")
				assert.Equals(t, sc.StatusCode(), tt.code)
			}
		})
	}
}

func TestJWK_AuthorizeSSHSign(t *testing.T) {
	tm, fn := mockNow()
	defer fn()

	p1, err := generateJWK()
	assert.FatalError(t, err)
	p2, err := generateJWK()
	assert.FatalError(t, err)
	// disable sshCA
	disable := false
	p2.Claims = &Claims{EnableSSHCA: &disable}
	p2.claimer, err = NewClaimer(p2.Claims, globalProvisionerClaims)
	assert.FatalError(t, err)

	jwk, err := decryptJSONWebKey(p1.EncryptedKey)
	assert.FatalError(t, err)

	iss, aud := p1.Name, testAudiences.SSHSign[0]

	t1, err := generateSimpleSSHUserToken(iss, aud, jwk)
	assert.FatalError(t, err)

	t2, err := generateSimpleSSHHostToken(iss, aud, jwk)
	assert.FatalError(t, err)

	// invalid signature
	failSig := t1[0 : len(t1)-2]

	key, err := generateJSONWebKey()
	assert.FatalError(t, err)

	signer, err := generateJSONWebKey()
	assert.FatalError(t, err)

	pub := key.Public().Key
	rsa2048, err := rsa.GenerateKey(rand.Reader, 2048)
	assert.FatalError(t, err)
	rsa1024, err := rsa.GenerateKey(rand.Reader, 1024)
	assert.FatalError(t, err)

	userDuration := p1.claimer.DefaultUserSSHCertDuration()
	hostDuration := p1.claimer.DefaultHostSSHCertDuration()
	expectedUserOptions := &SSHOptions{
		CertType: "user", Principals: []string{"name"},
		ValidAfter: NewTimeDuration(tm), ValidBefore: NewTimeDuration(tm.Add(userDuration)),
	}
	expectedHostOptions := &SSHOptions{
		CertType: "host", Principals: []string{"smallstep.com"},
		ValidAfter: NewTimeDuration(tm), ValidBefore: NewTimeDuration(tm.Add(hostDuration)),
	}

	type args struct {
		token   string
		sshOpts SSHOptions
		key     interface{}
	}
	tests := []struct {
		name        string
		prov        *JWK
		args        args
		expected    *SSHOptions
		code        int
		wantErr     bool
		wantSignErr bool
	}{
		{"user", p1, args{t1, SSHOptions{}, pub}, expectedUserOptions, http.StatusOK, false, false},
		{"user-rsa2048", p1, args{t1, SSHOptions{}, rsa2048.Public()}, expectedUserOptions, http.StatusOK, false, false},
		{"user-type", p1, args{t1, SSHOptions{CertType: "user"}, pub}, expectedUserOptions, http.StatusOK, false, false},
		{"user-principals", p1, args{t1, SSHOptions{Principals: []string{"name"}}, pub}, expectedUserOptions, http.StatusOK, false, false},
		{"user-options", p1, args{t1, SSHOptions{CertType: "user", Principals: []string{"name"}}, pub}, expectedUserOptions, http.StatusOK, false, false},
		{"host", p1, args{t2, SSHOptions{}, pub}, expectedHostOptions, http.StatusOK, false, false},
		{"host-type", p1, args{t2, SSHOptions{CertType: "host"}, pub}, expectedHostOptions, http.StatusOK, false, false},
		{"host-principals", p1, args{t2, SSHOptions{Principals: []string{"smallstep.com"}}, pub}, expectedHostOptions, http.StatusOK, false, false},
		{"host-options", p1, args{t2, SSHOptions{CertType: "host", Principals: []string{"smallstep.com"}}, pub}, expectedHostOptions, http.StatusOK, false, false},
		{"fail-sshCA-disabled", p2, args{"foo", SSHOptions{}, pub}, expectedUserOptions, http.StatusUnauthorized, true, false},
		{"fail-signature", p1, args{failSig, SSHOptions{}, pub}, nil, http.StatusUnauthorized, true, false},
		{"rail-rsa1024", p1, args{t1, SSHOptions{}, rsa1024.Public()}, expectedUserOptions, http.StatusOK, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.prov.AuthorizeSSHSign(context.Background(), tt.args.token)
			if (err != nil) != tt.wantErr {
				t.Errorf("JWK.AuthorizeSSHSign() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil {
				sc, ok := err.(errs.StatusCoder)
				assert.Fatal(t, ok, "error does not implement StatusCoder interface")
				assert.Equals(t, sc.StatusCode(), tt.code)
				assert.Nil(t, got)
			} else if assert.NotNil(t, got) {
				cert, err := signSSHCertificate(tt.args.key, tt.args.sshOpts, got, signer.Key.(crypto.Signer))
				if (err != nil) != tt.wantSignErr {
					t.Errorf("SignSSH error = %v, wantSignErr %v", err, tt.wantSignErr)
				} else {
					if tt.wantSignErr {
						assert.Nil(t, cert)
					} else {
						assert.NoError(t, validateSSHCertificate(cert, tt.expected))
					}
				}
			}
		})
	}
}

func TestJWK_AuthorizeSign_SSHOptions(t *testing.T) {
	tm, fn := mockNow()
	defer fn()

	p1, err := generateJWK()
	assert.FatalError(t, err)
	jwk, err := decryptJSONWebKey(p1.EncryptedKey)
	assert.FatalError(t, err)

	sub, iss, aud, iat := "subject@smallstep.com", p1.Name, testAudiences.SSHSign[0], time.Now()

	key, err := generateJSONWebKey()
	assert.FatalError(t, err)

	signer, err := generateJSONWebKey()
	assert.FatalError(t, err)

	userDuration := p1.claimer.DefaultUserSSHCertDuration()
	hostDuration := p1.claimer.DefaultHostSSHCertDuration()
	expectedUserOptions := &SSHOptions{
		CertType: "user", Principals: []string{"name"},
		ValidAfter: NewTimeDuration(tm), ValidBefore: NewTimeDuration(tm.Add(userDuration)),
	}
	expectedHostOptions := &SSHOptions{
		CertType: "host", Principals: []string{"smallstep.com"},
		ValidAfter: NewTimeDuration(tm), ValidBefore: NewTimeDuration(tm.Add(hostDuration)),
	}
	type args struct {
		sub, iss, aud string
		iat           time.Time
		tokSSHOpts    *SSHOptions
		userSSHOpts   *SSHOptions
		jwk           *jose.JSONWebKey
	}
	tests := []struct {
		name        string
		prov        *JWK
		args        args
		expected    *SSHOptions
		wantErr     bool
		wantSignErr bool
	}{
		{"ok-user", p1, args{sub, iss, aud, iat, &SSHOptions{CertType: "user", Principals: []string{"name"}}, &SSHOptions{}, jwk}, expectedUserOptions, false, false},
		{"ok-host", p1, args{sub, iss, aud, iat, &SSHOptions{CertType: "host", Principals: []string{"smallstep.com"}}, &SSHOptions{}, jwk}, expectedHostOptions, false, false},
		{"ok-user-opts", p1, args{sub, iss, aud, iat, &SSHOptions{}, &SSHOptions{CertType: "user", Principals: []string{"name"}}, jwk}, expectedUserOptions, false, false},
		{"ok-host-opts", p1, args{sub, iss, aud, iat, &SSHOptions{}, &SSHOptions{CertType: "host", Principals: []string{"smallstep.com"}}, jwk}, expectedHostOptions, false, false},
		{"ok-user-mixed", p1, args{sub, iss, aud, iat, &SSHOptions{CertType: "user"}, &SSHOptions{Principals: []string{"name"}}, jwk}, expectedUserOptions, false, false},
		{"ok-host-mixed", p1, args{sub, iss, aud, iat, &SSHOptions{Principals: []string{"smallstep.com"}}, &SSHOptions{CertType: "host"}, jwk}, expectedHostOptions, false, false},
		{"ok-user-validAfter", p1, args{sub, iss, aud, iat, &SSHOptions{
			CertType: "user", Principals: []string{"name"},
		}, &SSHOptions{
			ValidAfter: NewTimeDuration(tm.Add(-time.Hour)),
		}, jwk}, &SSHOptions{
			CertType: "user", Principals: []string{"name"}, ValidAfter: NewTimeDuration(tm.Add(-time.Hour)), ValidBefore: NewTimeDuration(tm.Add(userDuration - time.Hour)),
		}, false, false},
		{"ok-user-validBefore", p1, args{sub, iss, aud, iat, &SSHOptions{
			CertType: "user", Principals: []string{"name"},
		}, &SSHOptions{
			ValidBefore: NewTimeDuration(tm.Add(time.Hour)),
		}, jwk}, &SSHOptions{
			CertType: "user", Principals: []string{"name"}, ValidAfter: NewTimeDuration(tm), ValidBefore: NewTimeDuration(tm.Add(time.Hour)),
		}, false, false},
		{"ok-user-validAfter-validBefore", p1, args{sub, iss, aud, iat, &SSHOptions{
			CertType: "user", Principals: []string{"name"},
		}, &SSHOptions{
			ValidAfter: NewTimeDuration(tm.Add(10 * time.Minute)), ValidBefore: NewTimeDuration(tm.Add(time.Hour)),
		}, jwk}, &SSHOptions{
			CertType: "user", Principals: []string{"name"}, ValidAfter: NewTimeDuration(tm.Add(10 * time.Minute)), ValidBefore: NewTimeDuration(tm.Add(time.Hour)),
		}, false, false},
		{"ok-user-match", p1, args{sub, iss, aud, iat, &SSHOptions{
			CertType: "user", Principals: []string{"name"}, ValidAfter: NewTimeDuration(tm), ValidBefore: NewTimeDuration(tm.Add(1 * time.Hour)),
		}, &SSHOptions{
			CertType: "user", Principals: []string{"name"}, ValidAfter: NewTimeDuration(tm), ValidBefore: NewTimeDuration(tm.Add(1 * time.Hour)),
		}, jwk}, &SSHOptions{
			CertType: "user", Principals: []string{"name"}, ValidAfter: NewTimeDuration(tm), ValidBefore: NewTimeDuration(tm.Add(time.Hour)),
		}, false, false},
		{"fail-certType", p1, args{sub, iss, aud, iat, &SSHOptions{CertType: "user", Principals: []string{"name"}}, &SSHOptions{CertType: "host"}, jwk}, nil, false, true},
		{"fail-principals", p1, args{sub, iss, aud, iat, &SSHOptions{CertType: "user", Principals: []string{"name"}}, &SSHOptions{Principals: []string{"root"}}, jwk}, nil, false, true},
		{"fail-validAfter", p1, args{sub, iss, aud, iat, &SSHOptions{CertType: "user", Principals: []string{"name"}, ValidAfter: NewTimeDuration(tm)}, &SSHOptions{ValidAfter: NewTimeDuration(tm.Add(time.Hour))}, jwk}, nil, false, true},
		{"fail-validBefore", p1, args{sub, iss, aud, iat, &SSHOptions{CertType: "user", Principals: []string{"name"}, ValidBefore: NewTimeDuration(tm.Add(time.Hour))}, &SSHOptions{ValidBefore: NewTimeDuration(tm.Add(10 * time.Hour))}, jwk}, nil, false, true},
		{"fail-subject", p1, args{"", iss, aud, iat, &SSHOptions{CertType: "user", Principals: []string{"name"}}, &SSHOptions{}, jwk}, nil, true, false},
		{"fail-issuer", p1, args{sub, "invalid", aud, iat, &SSHOptions{CertType: "user", Principals: []string{"name"}}, &SSHOptions{}, jwk}, nil, true, false},
		{"fail-audience", p1, args{sub, iss, "invalid", iat, &SSHOptions{CertType: "user", Principals: []string{"name"}}, &SSHOptions{}, jwk}, nil, true, false},
		{"fail-expired", p1, args{sub, iss, aud, iat.Add(-6 * time.Minute), &SSHOptions{CertType: "user", Principals: []string{"name"}}, &SSHOptions{}, jwk}, nil, true, false},
		{"fail-notBefore", p1, args{sub, iss, aud, iat.Add(5 * time.Minute), &SSHOptions{CertType: "user", Principals: []string{"name"}}, &SSHOptions{}, jwk}, nil, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token, err := generateSSHToken(tt.args.sub, tt.args.iss, tt.args.aud, tt.args.iat, tt.args.tokSSHOpts, tt.args.jwk)
			assert.FatalError(t, err)
			if got, err := tt.prov.AuthorizeSSHSign(context.Background(), token); (err != nil) != tt.wantErr {
				t.Errorf("JWK.AuthorizeSSHSign() error = %v, wantErr %v", err, tt.wantErr)
			} else if !tt.wantErr && assert.NotNil(t, got) {
				var opts SSHOptions
				if tt.args.userSSHOpts != nil {
					opts = *tt.args.userSSHOpts
				}
				cert, err := signSSHCertificate(key.Public().Key, opts, got, signer.Key.(crypto.Signer))
				if (err != nil) != tt.wantSignErr {
					t.Errorf("SignSSH error = %v, wantSignErr %v", err, tt.wantSignErr)
				} else {
					if tt.wantSignErr {
						assert.Nil(t, cert)
					} else {
						assert.NoError(t, validateSSHCertificate(cert, tt.expected))
					}
				}
			}
		})
	}
}

func TestJWK_AuthorizeSSHRevoke(t *testing.T) {
	type test struct {
		p     *JWK
		token string
		code  int
		err   error
	}
	tests := map[string]func(*testing.T) test{
		"fail/invalid-token": func(t *testing.T) test {
			p, err := generateJWK()
			assert.FatalError(t, err)
			return test{
				p:     p,
				token: "foo",
				code:  http.StatusUnauthorized,
				err:   errors.New("jwk.AuthorizeSSHRevoke: jwk.authorizeToken; error parsing jwk token"),
			}
		},
		"ok": func(t *testing.T) test {
			p, err := generateJWK()
			assert.FatalError(t, err)
			jwk, err := decryptJSONWebKey(p.EncryptedKey)
			assert.FatalError(t, err)

			tok, err := generateToken("subject", p.Name, testAudiences.SSHRevoke[0], "name@smallstep.com", []string{"127.0.0.1", "max@smallstep.com", "foo"}, time.Now(), jwk)
			assert.FatalError(t, err)
			return test{
				p:     p,
				token: tok,
			}
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			tc := tt(t)
			if err := tc.p.AuthorizeSSHRevoke(context.Background(), tc.token); err != nil {
				if assert.NotNil(t, tc.err) {
					sc, ok := err.(errs.StatusCoder)
					assert.Fatal(t, ok, "error does not implement StatusCoder interface")
					assert.Equals(t, sc.StatusCode(), tc.code)
					assert.HasPrefix(t, err.Error(), tc.err.Error())
				}
			} else {
				assert.Nil(t, tc.err)
			}
		})
	}
}
