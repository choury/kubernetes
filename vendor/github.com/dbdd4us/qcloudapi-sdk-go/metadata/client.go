package metadata

import (
	"errors"
	"fmt"
	. "github.com/dbdd4us/qcloudapi-sdk-go/util"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/golang/glog"
)

type Request struct {
}

const (
	ENDPOINT = "http://metadata.tencentyun.com/meta-data"

	INSTANCE_ID  = "instance-id"
	UUID         = "uuid"
	MAC          = "mac"
	PRIVATE_IPV4 = "local-ipv4"
	REGION       = "placement/region"
	ZONE         = "placement/zone"
	PUBLIC_IPV4  = "public-ipv4"
)

type IMetaDataClient interface {
	Go(resource string) (string, error)
	Url(resource string) (string, error)
}

type MetaData struct {
	c IMetaDataClient
}

func NewMetaData(client *http.Client) *MetaData {
	if client == nil {
		client = &http.Client{}
	}
	return &MetaData{
		c: &MetaDataClient{client: client},
	}
}

func (m *MetaData) UUID() (string, error) {

	uuid, err := m.c.Go(UUID)
	if err != nil {
		return "", err
	}
	return uuid, err
}

func (m *MetaData) InstanceID() (string, error) {

	instanceId, err := m.c.Go(INSTANCE_ID)
	if err != nil {
		return "", err
	}
	return instanceId, err
}

func (m *MetaData) Mac() (string, error) {

	mac, err := m.c.Go(MAC)
	if err != nil {
		return "", err
	}
	return mac, nil
}

func (m *MetaData) PrivateIPv4() (string, error) {

	ip, err := m.c.Go(PRIVATE_IPV4)
	if err != nil {
		return "", err
	}
	return ip, nil
}

func (m *MetaData) PublicIPv4() (string, error) {

	ip, err := m.c.Go(PUBLIC_IPV4)
	if err != nil {
		return "", err
	}
	return ip, nil
}

func (m *MetaData) Region() (string, error) {

	region, err := m.c.Go(REGION)
	if err != nil {
		return "", err
	}
	return region, nil
}

func (m *MetaData) Zone() (string, error) {

	zone, err := m.c.Go(ZONE)
	if err != nil {
		return "", err
	}
	return zone, nil
}

//
type MetaDataClient struct {
	client   *http.Client
}

func (m *MetaDataClient) Url(resource string) (string, error) {
	if resource == "" {
		return "", errors.New("the resource you want to visit must not be nil!")
	}
	return fmt.Sprintf("%s/%s", ENDPOINT, resource), nil
}

func (m *MetaDataClient) send(resource string) (string, error) {
	u, err := m.Url(resource)
	if err != nil {
		return "", err
	}

	glog.V(2).Infof("MetaDataClient send resource %s  url %s",resource,u)

	requ, err := http.NewRequest(http.MethodGet, u, nil)

	if err != nil {
		return "", err
	}
	resp, err := m.client.Do(requ)
	if err != nil {
		return "", err
	}

	glog.V(2).Infof("MetaDataClient resource %s StatusCode %d",resource,resp.StatusCode)

	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("MetaDataClient resource %s  StatusCode %d",resource,resp.StatusCode)
	}
	defer resp.Body.Close()

	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	glog.V(2).Infof("MetaDataClient resource %s send data %s",resource,string(data))
	
	return string(data), nil

}

var retry = AttemptStrategy{
	Min:   5,
	Total: 5 * time.Second,
	Delay: 200 * time.Millisecond,
}

func (vpc *MetaDataClient) Go(resource string) (resu string, err error) {
	for r := retry.Start(); r.Next(); {
		resu, err = vpc.send(resource)
		if !shouldRetry(err) {
			break
		}
	}
	return resu, err
}

type TimeoutError interface {
	error
	Timeout() bool // Is the error a timeout?
}

func shouldRetry(err error) bool {
	if err == nil {
		return false
	}

	glog.Errorf("MetaDataClient shouldRetry %s",err.Error())

	_, ok := err.(TimeoutError)
	if ok {
		return true
	}

	switch err {
	case io.ErrUnexpectedEOF, io.EOF:
		return true
	}
	switch e := err.(type) {
	case *net.DNSError:
		return true
	case *net.OpError:
		switch e.Op {
		case "read", "write":
			return true
		}
	case *url.Error:
		// url.Error can be returned either by net/url if a URL cannot be
		// parsed, or by net/http if the response is closed before the headers
		// are received or parsed correctly. In that later case, e.Op is set to
		// the HTTP method name with the first letter uppercased. We don't want
		// to retry on POST operations, since those are not idempotent, all the
		// other ones should be safe to retry.
		switch e.Op {
		case "Get", "Put", "Delete", "Head":
			return shouldRetry(e.Err)
		default:
			return false
		}
	}
	return false
}
