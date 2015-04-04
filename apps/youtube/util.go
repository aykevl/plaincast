package youtube

import (
	"errors"
	"io"
	"io/ioutil"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
)

// zx generates a random string of bytes that is 12 characters long.
// It is being used by some (unofficial) Google APIs.
func zx() []byte {
	buf := make([]byte, 12)
	for i, _ := range buf {
		buf[i] = 'a' + byte(rand.Intn(26))
	}

	return buf
}

// httpGetBody issues an HTTP request and returns the response body as a byte
// array, or an error on HTTP protocol erroros or when the HTTP status code
// isn't 200.
func httpGetBody(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return processRequest(resp)
}

// httpPostFormBody is similar to httpGetBody, but does a POST request with
// the supplied values.
func httpPostFormBody(url string, values url.Values) ([]byte, error) {
	resp, err := http.PostForm(url, values)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return processRequest(resp)
}

// processRequest downloads a HTTP response body. It will return an error when
// the HTTP status code isn't 200.
func processRequest(resp *http.Response) ([]byte, error) {

	if resp.StatusCode != 200 {
		return nil, errors.New("unexpected HTTP status code: " + strconv.Itoa(resp.StatusCode))
	}

	if resp.ContentLength < 0 {
		return ioutil.ReadAll(resp.Body)
	} else {
		buf := make([]byte, resp.ContentLength)
		_, err := io.ReadFull(resp.Body, buf)
		if err != nil {
			return nil, err
		}
		return buf, nil
	}
}

// handle is a helper function for easier handling of fatal errors.
func handle(err error, message string) {
	if err != nil {
		logger.Fatalf("%s: %s\n", message, err)
	}
}
