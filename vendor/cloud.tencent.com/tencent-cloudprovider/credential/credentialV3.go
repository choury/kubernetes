package credential

import (
	"sync"
	"time"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
)
var _ common.CredentialV3Interface = &NormCredentialV3{}

type NormCredentialV3 struct {
	secretId        string
	secretKey       string
	token           string

	expiredAt       time.Time
	expiredDuration time.Duration // TODO: maybe confused with this? find a better way

	lock            sync.Mutex

	refresher       Refresher
}

func NewNormCredentialV3(expiredDuration time.Duration, refresher Refresher) (NormCredentialV3, error) {
	return NormCredentialV3{
		expiredDuration: expiredDuration,
		refresher:       refresher,
	}, nil
}

func (normCred *NormCredentialV3) GetSecretId() (string, error) {
	if normCred.needRefresh() {
		if err := normCred.refresh(); err != nil {
			return "", err
		}
	}
	return normCred.secretId, nil
}

func (normCred *NormCredentialV3) GetSecretKey() (string, error) {
	if normCred.needRefresh() {
		if err := normCred.refresh(); err != nil {
			return "", err
		}
	}
	return normCred.secretKey, nil
}

func (normCred *NormCredentialV3) GetToken() (string, error) {
	if normCred.needRefresh() {
		if err := normCred.refresh(); err != nil {
			return "", err
		}
	}
	return normCred.token, nil
}

func (normCred *NormCredentialV3) GetCredentialParams() (map[string]string, error) {
	if normCred.needRefresh() {
		if err := normCred.refresh(); err != nil {
			return nil, err
		}
	}
	return map[string]string{"SecretId": normCred.secretId, "Token": normCred.token}, nil
}



func (normCred *NormCredentialV3) needRefresh() bool {
	return time.Now().Add(normCred.expiredDuration / time.Second / 2 * time.Second).After(normCred.expiredAt)
}

func (normCred *NormCredentialV3) refresh() error {
	secretId, secretKey, token, expiredAt, err := normCred.refresher.Refresh()
	if err != nil {
		return err
	}

	normCred.updateExpiredAt(time.Unix(int64(expiredAt), 0))
	normCred.updateCredential(secretId, secretKey, token)

	return nil
}

func (normCred *NormCredentialV3) updateExpiredAt(expiredAt time.Time) {
	normCred.lock.Lock()
	defer normCred.lock.Unlock()

	normCred.expiredAt = expiredAt
}

func (normCred *NormCredentialV3) updateCredential(secretId, secretKey, token string) {
	normCred.lock.Lock()
	defer normCred.lock.Unlock()

	normCred.secretId = secretId
	normCred.secretKey = secretKey
	normCred.token = token
}
