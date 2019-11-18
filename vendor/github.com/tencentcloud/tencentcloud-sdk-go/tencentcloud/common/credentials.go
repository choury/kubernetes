package common



type CredentialV3Interface interface {
	GetSecretId() (string, error)
	GetSecretKey() (string, error)
	GetToken() (string, error)
 	GetCredentialParams() (map[string]string, error)
}

type Credential struct {
	SecretId  string
	SecretKey string
	Token     string
}

func NewCredential(secretId, secretKey string) *Credential {
	return &Credential{
		SecretId:  secretId,
		SecretKey: secretKey,
	}
}

func NewTokenCredential(secretId, secretKey, token string) *Credential {
	return &Credential{
		SecretId:  secretId,
		SecretKey: secretKey,
		Token:     token,
	}
}

func (c *Credential) GetSecretId() (string, error) {
	return c.SecretId, nil
}

func (c *Credential) GetSecretKey() (string, error) {
	return c.SecretKey, nil
}

func (c *Credential) GetToken() (string, error) {
	return c.Token, nil
}

func (c *Credential) GetCredentialParams() (map[string]string, error) {
	p := map[string]string{
		"SecretId": c.SecretId,
	}
	if c.Token != "" {
		p["Token"] = c.Token
	}
	return p, nil
}

// Nowhere use them and we haven't well designed these structures and
// underlying method, which leads to the situation that it is hard to
// refactor it to interfaces.
// Hence they are removed and merged into Credential.

//type TokenCredential struct {
//	SecretId  string
//	SecretKey string
//	Token     string
//}

//func NewTokenCredential(secretId, secretKey, token string) *TokenCredential {
//	return &TokenCredential{
//		SecretId:  secretId,
//		SecretKey: secretKey,
//		Token:     token,
//	}
//}

//func (c *TokenCredential) GetCredentialParams() map[string]string {
//	return map[string]string{
//		"SecretId": c.SecretId,
//		"Token":    c.Token,
//	}
//}
