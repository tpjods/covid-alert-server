package server

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"github.com/Shopify/goose/logger"
	persistence "github.com/cds-snc/covid-alert-server/mocks/pkg/persistence"
	persistenceErrors "github.com/cds-snc/covid-alert-server/pkg/persistence"
	pb "github.com/cds-snc/covid-alert-server/pkg/proto/covidshield"
	"github.com/cds-snc/covid-alert-server/pkg/testhelpers"
	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/mock"
	"golang.org/x/crypto/nacl/box"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func setupUploadRouter(db *persistence.Conn) *mux.Router {

	servlet := NewUploadServlet(db)
	router := Router()
	servlet.RegisterRouting(router)

	return router
}

func TestNewUploadServlet(t *testing.T) {
	db := &persistence.Conn{}

	expected := &uploadServlet{
		db: db,
	}
	assert.Equal(t, expected, NewUploadServlet(db), "should return a new uploadServlet struct")
}

func TestRegisterRoutingUpload(t *testing.T) {
	router := setupUploadRouter(&persistence.Conn{})
	expectedPaths := GetPaths(router)
	assert.Contains(t, expectedPaths, "/upload", "should include an upload path")
}

func TestUploadError(t *testing.T) {
	err := pb.EncryptedUploadResponse_UNKNOWN
	expected := &pb.EncryptedUploadResponse{Error: &err}
	assert.Equal(t, expected, uploadError(err), "should wrap the upload error code in an upload error response")
}

func setupUploadTest() (*test.Hook, *logger.Logger, *persistence.Conn, *mux.Router) {

	hook, oldLog := testhelpers.SetupTestLogging(&log)
	db := &persistence.Conn{}
	router := setupUploadRouter(db)

	return hook, oldLog, db, router

}

func TestUpload_NonProtoBufPayload(t *testing.T) {
	hook, oldLog, _, router := setupUploadTest()
	defer func() { log = *oldLog }()

	// Bad, non-protobuff payload
	req, _ := http.NewRequest("POST", "/upload", strings.NewReader("sd"))
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	assert.Equal(t, 400, resp.Code, "400 response is expected")
	assert.True(t, checkUploadResponse(resp.Body.Bytes(), pb.EncryptedUploadResponse_UNKNOWN))

	testhelpers.AssertLog(t, hook, 1, logrus.WarnLevel, "error unmarshalling request")
}

func TestUpload_PublicCertTooShort(t *testing.T) {

	hook, oldLog, _, router := setupUploadTest()
	defer func() { log = *oldLog }()

	// Server Public cert too short
	payload, _ := proto.Marshal(buildUploadRequest(make([]byte, 16), nil, nil, nil))
	req, _ := http.NewRequest("POST", "/upload", bytes.NewReader(payload))
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	assert.Equal(t, 400, resp.Code, "400 response is expected")
	assert.True(t, checkUploadResponse(resp.Body.Bytes(), pb.EncryptedUploadResponse_INVALID_CRYPTO_PARAMETERS))

	testhelpers.AssertLog(t, hook, 1, logrus.WarnLevel, "server public key was not expected length")
}

func TestUpload_PublicCertNotFound(t *testing.T) {

	hook, oldLog, db, router := setupUploadTest()
	defer func() { log = *oldLog }()

	badServerPub, _, _ := box.GenerateKey(rand.Reader)
	db.On("PrivForPub", badServerPub[:]).Return(nil, fmt.Errorf("No priv cert"))

	// Public cert not found
	payload, _ := proto.Marshal(buildUploadRequest(badServerPub[:], nil, nil, nil))
	req, _ := http.NewRequest("POST", "/upload", bytes.NewReader(payload))
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	assert.Equal(t, 401, resp.Code, "401 response is expected")
	assert.True(t, checkUploadResponse(resp.Body.Bytes(), pb.EncryptedUploadResponse_INVALID_KEYPAIR))

	testhelpers.AssertLog(t, hook, 1, logrus.WarnLevel, "failure to resolve client keypair")
}

func TestUpload_IncorrectNonceLength(t *testing.T) {

	hook, oldLog, db, router := setupUploadTest()
	defer func() { log = *oldLog }()

	goodServerPub, goodServerPriv, _ := box.GenerateKey(rand.Reader)
	db.On("PrivForPub", goodServerPub[:]).Return(goodServerPriv[:], nil)

	// Nonce incorrect length
	payload, _ := proto.Marshal(buildUploadRequest(goodServerPub[:], make([]byte, 16), nil, nil))
	req, _ := http.NewRequest("POST", "/upload", bytes.NewReader(payload))
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	assert.Equal(t, 400, resp.Code, "400 response is expected")
	assert.True(t, checkUploadResponse(resp.Body.Bytes(), pb.EncryptedUploadResponse_INVALID_CRYPTO_PARAMETERS))

	testhelpers.AssertLog(t, hook, 1, logrus.WarnLevel, "nonce was not expected length")
}

func TestUpload_AppPublicCertTooShort(t *testing.T) {
	hook, oldLog, db, router := setupUploadTest()
	defer func() { log = *oldLog }()

	goodServerPub, goodServerPriv, _ := box.GenerateKey(rand.Reader)
	db.On("PrivForPub", goodServerPub[:]).Return(goodServerPriv[:], nil)

	// App Public cert too short
	payload, _ := proto.Marshal(buildUploadRequest(goodServerPub[:], make([]byte, 24), make([]byte, 16), nil))
	req, _ := http.NewRequest("POST", "/upload", bytes.NewReader(payload))
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	assert.Equal(t, 400, resp.Code, "400 response is expected")
	assert.True(t, checkUploadResponse(resp.Body.Bytes(), pb.EncryptedUploadResponse_INVALID_CRYPTO_PARAMETERS))

	testhelpers.AssertLog(t, hook, 1, logrus.WarnLevel, "app public key key was not expected length")
}

func TestUpload_ServerPrivateCertTooShort(t *testing.T) {
	hook, oldLog, db, router := setupUploadTest()
	defer func() { log = *oldLog }()

	goodServerPubBadPriv, _, _ := box.GenerateKey(rand.Reader)
	db.On("PrivForPub", goodServerPubBadPriv[:]).Return(make([]byte, 16), nil)

	// Server private cert too short
	payload, _ := proto.Marshal(buildUploadRequest(goodServerPubBadPriv[:], make([]byte, 24), make([]byte, 32), nil))
	req, _ := http.NewRequest("POST", "/upload", bytes.NewReader(payload))
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	assert.Equal(t, 500, resp.Code, "500 response is expected")
	assert.True(t, checkUploadResponse(resp.Body.Bytes(), pb.EncryptedUploadResponse_SERVER_ERROR))

	testhelpers.AssertLog(t, hook, 1, logrus.ErrorLevel, "server private key was not expected length")
}

func TestUpload_FailsToDecryptPayload(t *testing.T) {
	hook, oldLog, db, router := setupUploadTest()
	defer func() { log = *oldLog }()

	goodAppPub, _, _ := box.GenerateKey(rand.Reader)
	badServerPub, _, _ := box.GenerateKey(rand.Reader)
	goodServerPub, goodServerPriv, _ := box.GenerateKey(rand.Reader)

	db.On("PrivForPub", goodServerPub[:]).Return(goodServerPriv[:], nil)
	db.On("PrivForPub", badServerPub[:]).Return(nil, fmt.Errorf("No priv cert"))

	// Fails to decrypt payload
	var (
		nonce [24]byte
		msg   []byte
	)
	io.ReadFull(rand.Reader, nonce[:])
	encrypted := box.Seal(msg[:], []byte("hello world"), &nonce, goodAppPub, badServerPub)

	payload, _ := proto.Marshal(buildUploadRequest(goodServerPub[:], nonce[:], goodAppPub[:], encrypted))
	req, _ := http.NewRequest("POST", "/upload", bytes.NewReader(payload))
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	assert.Equal(t, 400, resp.Code, "400 response is expected")
	assert.True(t, checkUploadResponse(resp.Body.Bytes(), pb.EncryptedUploadResponse_DECRYPTION_FAILED))

	testhelpers.AssertLog(t, hook, 1, logrus.WarnLevel, "failure to decrypt payload")
}

func TestUpload_FailsUnmarshalIntoUpload(t *testing.T) {

	hook, oldLog, db, router := setupUploadTest()
	defer func() { log = *oldLog }()

	goodAppPub, _, _ := box.GenerateKey(rand.Reader)
	badServerPub, _, _ := box.GenerateKey(rand.Reader)
	goodServerPub, goodServerPriv, _ := box.GenerateKey(rand.Reader)

	db.On("PrivForPub", goodServerPub[:]).Return(goodServerPriv[:], nil)
	db.On("PrivForPub", badServerPub[:]).Return(nil, fmt.Errorf("No priv cert"))

	// Fails unmarshall into Upload
	var (
		nonce [24]byte
		msg   []byte
	)

	io.ReadFull(rand.Reader, nonce[:])
	encrypted := box.Seal(msg[:], []byte("hello world"), &nonce, goodAppPub, goodServerPriv)

	payload, _ := proto.Marshal(buildUploadRequest(goodServerPub[:], nonce[:], goodAppPub[:], encrypted))
	req, _ := http.NewRequest("POST", "/upload", bytes.NewReader(payload))
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	assert.Equal(t, 400, resp.Code, "400 response is expected")
	assert.True(t, checkUploadResponse(resp.Body.Bytes(), pb.EncryptedUploadResponse_INVALID_PAYLOAD))

	testhelpers.AssertLog(t, hook, 1, logrus.WarnLevel, "error unmarshalling request payload")
}

func TestUpload_NoKeysInPayload(t *testing.T) {
	hook, oldLog, db, router := setupUploadTest()
	defer func() { log = *oldLog }()
	goodServerPub, goodServerPriv, _ := box.GenerateKey(rand.Reader)
	goodAppPub, goodAppPriv, _ := box.GenerateKey(rand.Reader)

	db.On("PrivForPub", goodServerPub[:]).Return(goodServerPriv[:], nil)
	db.On("StoreKeys", goodAppPub, mock.AnythingOfType("[]*covidshield.TemporaryExposureKey"), mock.Anything).Return(nil)

	var (
		nonce [24]byte
		msg   []byte
	)
	// No keys in payload
	io.ReadFull(rand.Reader, nonce[:])
	ts := time.Now()
	pbts := timestamppb.Timestamp{
		Seconds: ts.Unix(),
	}
	upload := buildUpload(0, pbts)
	marshalledUpload, _ := proto.Marshal(upload)
	encrypted := box.Seal(msg[:], marshalledUpload, &nonce, goodServerPub, goodAppPriv)

	payload, _ := proto.Marshal(buildUploadRequest(goodServerPub[:], nonce[:], goodAppPub[:], encrypted))
	req, _ := http.NewRequest("POST", "/upload", bytes.NewReader(payload))
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	assert.Equal(t, 400, resp.Code, "400 response is expected")
	assert.True(t, checkUploadResponse(resp.Body.Bytes(), pb.EncryptedUploadResponse_NO_KEYS_IN_PAYLOAD))

	testhelpers.AssertLog(t, hook, 1, logrus.WarnLevel, "no keys provided")
}

func TestUpload_TooManyKeys(t *testing.T) {

	hook, oldLog, db, router := setupUploadTest()
	defer func() { log = *oldLog }()
	// Set up PrivForPub
	goodServerPub, goodServerPriv, _ := box.GenerateKey(rand.Reader)
	goodAppPub, goodAppPriv, _ := box.GenerateKey(rand.Reader)

	db.On("PrivForPub", goodServerPub[:]).Return(goodServerPriv[:], nil)
	db.On("StoreKeys", goodAppPub, mock.AnythingOfType("[]*covidshield.TemporaryExposureKey"), mock.Anything).Return(nil)
	var (
		nonce [24]byte
		msg   []byte
	)
	// Too many keys in payload

	io.ReadFull(rand.Reader, nonce[:])
	ts := time.Now()
	pbts := timestamppb.Timestamp{
		Seconds: ts.Unix(),
	}
	upload := buildUpload(pb.MaxKeysInUpload+1, pbts)
	marshalledUpload, _ := proto.Marshal(upload)
	encrypted := box.Seal(msg[:], marshalledUpload, &nonce, goodServerPub, goodAppPriv)

	payload, _ := proto.Marshal(buildUploadRequest(goodServerPub[:], nonce[:], goodAppPub[:], encrypted))
	req, _ := http.NewRequest("POST", "/upload", bytes.NewReader(payload))
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	assert.Equal(t, 400, resp.Code, "400 response is expected")
	assert.True(t, checkUploadResponse(resp.Body.Bytes(), pb.EncryptedUploadResponse_TOO_MANY_KEYS))

	testhelpers.AssertLog(t, hook, 1, logrus.WarnLevel, "too many keys provided")
}

func TestUpload_InvalidTimestamp(t *testing.T) {

	hook, oldLog, db, router := setupUploadTest()
	defer func() { log = *oldLog }()
	// Set up PrivForPub
	goodServerPub, goodServerPriv, _ := box.GenerateKey(rand.Reader)
	goodAppPub, goodAppPriv, _ := box.GenerateKey(rand.Reader)

	db.On("PrivForPub", goodServerPub[:]).Return(goodServerPriv[:], nil)
	db.On("StoreKeys", goodAppPub, mock.AnythingOfType("[]*covidshield.TemporaryExposureKey"), mock.Anything).Return(nil)

	var (
		nonce [24]byte
		msg   []byte
	)
	// Invalid timestamp
	io.ReadFull(rand.Reader, nonce[:])
	ts := time.Now()
	pbts := timestamppb.Timestamp{
		Seconds: ts.Unix() - 4000,
	}
	upload := buildUpload(pb.MaxKeysInUpload, pbts)
	marshalledUpload, _ := proto.Marshal(upload)
	encrypted := box.Seal(msg[:], marshalledUpload, &nonce, goodServerPub, goodAppPriv)

	payload, _ := proto.Marshal(buildUploadRequest(goodServerPub[:], nonce[:], goodAppPub[:], encrypted))
	req, _ := http.NewRequest("POST", "/upload", bytes.NewReader(payload))
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	assert.Equal(t, 400, resp.Code, "400 response is expected")
	assert.True(t, checkUploadResponse(resp.Body.Bytes(), pb.EncryptedUploadResponse_INVALID_TIMESTAMP))

	testhelpers.AssertLog(t, hook, 1, logrus.WarnLevel, "invalid timestamp")
}

func TestUpload_ExpiredKey(t *testing.T) {

	hook, oldLog, db, router := setupUploadTest()
	defer func() { log = *oldLog }()
	// Set up PrivForPub
	goodServerPub, goodServerPriv, _ := box.GenerateKey(rand.Reader)
	goodAppPubKeyUsed, goodAppPrivKeyUsed, _ := box.GenerateKey(rand.Reader)

	db.On("PrivForPub", goodServerPub[:]).Return(goodServerPriv[:], nil)
	db.On("StoreKeys", goodAppPubKeyUsed, mock.AnythingOfType("[]*covidshield.TemporaryExposureKey"), mock.Anything).Return(persistenceErrors.ErrKeyConsumed)

	var (
		nonce [24]byte
		msg   []byte
	)
	// Expired Key
	io.ReadFull(rand.Reader, nonce[:])
	ts := time.Now()
	pbts := timestamppb.Timestamp{
		Seconds: ts.Unix(),
	}
	upload := buildUpload(1, pbts)
	marshalledUpload, _ := proto.Marshal(upload)
	encrypted := box.Seal(msg[:], marshalledUpload, &nonce, goodServerPub, goodAppPrivKeyUsed)

	payload, _ := proto.Marshal(buildUploadRequest(goodServerPub[:], nonce[:], goodAppPubKeyUsed[:], encrypted))
	req, _ := http.NewRequest("POST", "/upload", bytes.NewReader(payload))
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	assert.Equal(t, 400, resp.Code, "400 response is expected")
	assert.True(t, checkUploadResponse(resp.Body.Bytes(), pb.EncryptedUploadResponse_INVALID_KEYPAIR))

	testhelpers.AssertLog(t, hook, 1, logrus.WarnLevel, "key is used up")
}

func TestUpload_GenericDBError(t *testing.T) {

	hook, oldLog, db, router := setupUploadTest()
	defer func() { log = *oldLog }()
	// Set up PrivForPub
	goodServerPub, goodServerPriv, _ := box.GenerateKey(rand.Reader)
	goodAppPubDBError, goodAppPrivDBError, _ := box.GenerateKey(rand.Reader)

	db.On("PrivForPub", goodServerPub[:]).Return(goodServerPriv[:], nil)
	db.On("StoreKeys", goodAppPubDBError, mock.AnythingOfType("[]*covidshield.TemporaryExposureKey"), mock.Anything).Return(fmt.Errorf("generic DB error"))

	var (
		nonce [24]byte
		msg   []byte
	)
	// Generic DB Error
	io.ReadFull(rand.Reader, nonce[:])
	ts := time.Now()
	pbts := timestamppb.Timestamp{
		Seconds: ts.Unix(),
	}
	upload := buildUpload(1, pbts)
	marshalledUpload, _ := proto.Marshal(upload)
	encrypted := box.Seal(msg[:], marshalledUpload, &nonce, goodServerPub, goodAppPrivDBError)

	payload, _ := proto.Marshal(buildUploadRequest(goodServerPub[:], nonce[:], goodAppPubDBError[:], encrypted))
	req, _ := http.NewRequest("POST", "/upload", bytes.NewReader(payload))
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	assert.Equal(t, 500, resp.Code, "500 response is expected")
	assert.True(t, checkUploadResponse(resp.Body.Bytes(), pb.EncryptedUploadResponse_SERVER_ERROR))

	testhelpers.AssertLog(t, hook, 1, logrus.ErrorLevel, "failed to store diagnosis keys")

}

func TestUpload_NotEnoughKeysRemaining(t *testing.T) {

	hook, oldLog, db, router := setupUploadTest()
	defer func() { log = *oldLog }()
	// Set up PrivForPub
	goodAppPubNoKeysRemaining, goodAppPrivNoKeysRemaining, _ := box.GenerateKey(rand.Reader)
	goodServerPubNoKeysRemaining, goodServerPrivNoKeysRemaining, _ := box.GenerateKey(rand.Reader)

	db.On("PrivForPub", goodServerPubNoKeysRemaining[:]).Return(goodServerPrivNoKeysRemaining[:], nil)
	db.On("StoreKeys", goodAppPubNoKeysRemaining, mock.AnythingOfType("[]*covidshield.TemporaryExposureKey"), mock.Anything).Return(persistenceErrors.ErrTooManyKeys)

	var (
		nonce [24]byte
		msg   []byte
	)
	// Not enough keys remaining
	io.ReadFull(rand.Reader, nonce[:])
	ts := time.Now()
	pbts := timestamppb.Timestamp{
		Seconds: ts.Unix(),
	}
	upload := buildUpload(1, pbts)
	marshalledUpload, _ := proto.Marshal(upload)
	encrypted := box.Seal(msg[:], marshalledUpload, &nonce, goodServerPubNoKeysRemaining, goodAppPrivNoKeysRemaining)

	payload, _ := proto.Marshal(buildUploadRequest(goodServerPubNoKeysRemaining[:], nonce[:], goodAppPubNoKeysRemaining[:], encrypted))
	req, _ := http.NewRequest("POST", "/upload", bytes.NewReader(payload))
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	assert.Equal(t, 400, resp.Code, "400 response is expected")
	assert.True(t, checkUploadResponse(resp.Body.Bytes(), pb.EncryptedUploadResponse_TOO_MANY_KEYS))

	testhelpers.AssertLog(t, hook, 1, logrus.WarnLevel, "not enough keys remaining")
}

func TestUpload(t *testing.T) {

	_, oldLog := testhelpers.SetupTestLogging(&log)
	defer func() { log = *oldLog }()

	db := &persistence.Conn{}
	router := setupUploadRouter(db)

	// Set up PrivForPub
	goodServerPub, goodServerPriv, _ := box.GenerateKey(rand.Reader)
	goodAppPub, goodAppPriv, _ := box.GenerateKey(rand.Reader)

	db.On("PrivForPub", goodServerPub[:]).Return(goodServerPriv[:], nil)
	db.On("StoreKeys", goodAppPub, mock.AnythingOfType("[]*covidshield.TemporaryExposureKey"), mock.Anything).Return(nil)

	var (
		nonce [24]byte
		msg   []byte
	)
	// Good response
	io.ReadFull(rand.Reader, nonce[:])
	ts := time.Now()
	pbts := timestamppb.Timestamp{
		Seconds: ts.Unix(),
	}
	upload := buildUpload(1, pbts)
	marshalledUpload, _ := proto.Marshal(upload)
	encrypted := box.Seal(msg[:], marshalledUpload, &nonce, goodServerPub, goodAppPriv)

	payload, _ := proto.Marshal(buildUploadRequest(goodServerPub[:], nonce[:], goodAppPub[:], encrypted))
	req, _ := http.NewRequest("POST", "/upload", bytes.NewReader(payload))
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	assert.Equal(t, 200, resp.Code, "200 response is expected")
	assert.True(t, checkUploadResponse(resp.Body.Bytes(), pb.EncryptedUploadResponse_NONE))
}

func TestValidateKey_RollingPeriodLT1(t *testing.T) {

	hook, oldLog := testhelpers.SetupTestLogging(&log)
	defer func() { log = *oldLog }()

	db := &persistence.Conn{}
	setupUploadRouter(db)
	req, _ := http.NewRequest("POST", "/upload", nil)
	resp := httptest.NewRecorder()

	// Test RollingPeriod < 1
	token := make([]byte, 16)
	rand.Read(token)
	key := buildKey(token, int32(2), int32(2651450), int32(0))

	result := validateKey(req.Context(), resp, &key)

	assert.False(t, result)

	assert.Equal(t, 400, resp.Code, "400 response is expected")
	assert.True(t, checkUploadResponse(resp.Body.Bytes(), pb.EncryptedUploadResponse_INVALID_ROLLING_PERIOD))

	testhelpers.AssertLog(t, hook, 1, logrus.WarnLevel, "missing or invalid rollingPeriod")
}

func TestValidateKey_RollingPeriodGT144(t *testing.T) {

	hook, oldLog := testhelpers.SetupTestLogging(&log)
	defer func() { log = *oldLog }()

	db := &persistence.Conn{}
	setupUploadRouter(db)

	req, _ := http.NewRequest("POST", "/upload", nil)
	resp := httptest.NewRecorder()

	// Test RollingPeriod > 144
	token := make([]byte, 16)
	rand.Read(token)
	key := buildKey(token, int32(2), int32(2651450), int32(145))

	result := validateKey(req.Context(), resp, &key)

	assert.False(t, result)

	assert.Equal(t, 400, resp.Code, "400 response is expected")
	assert.True(t, checkUploadResponse(resp.Body.Bytes(), pb.EncryptedUploadResponse_INVALID_ROLLING_PERIOD))

	testhelpers.AssertLog(t, hook, 1, logrus.WarnLevel, "missing or invalid rollingPeriod")

}

func TestValidateKey_KeyDataNot16Bytes(t *testing.T) {

	hook, oldLog := testhelpers.SetupTestLogging(&log)
	defer func() { log = *oldLog }()

	db := &persistence.Conn{}
	setupUploadRouter(db)
	req, _ := http.NewRequest("POST", "/upload", nil)
	resp := httptest.NewRecorder()

	// Key data not 16 bytes
	token := make([]byte, 8)
	rand.Read(token)
	key := buildKey(token, int32(2), int32(2651450), int32(144))

	result := validateKey(req.Context(), resp, &key)

	assert.False(t, result)

	assert.Equal(t, 400, resp.Code, "400 response is expected")
	assert.True(t, checkUploadResponse(resp.Body.Bytes(), pb.EncryptedUploadResponse_INVALID_KEY_DATA))

	testhelpers.AssertLog(t, hook, 1, logrus.WarnLevel, "invalid key data")

}

func TestValidateKey_InvalidRSIN(t *testing.T) {

	hook, oldLog := testhelpers.SetupTestLogging(&log)
	defer func() { log = *oldLog }()

	db := &persistence.Conn{}
	setupUploadRouter(db)
	req, _ := http.NewRequest("POST", "/upload", nil)
	resp := httptest.NewRecorder()
	// Invalid RSIN
	token := make([]byte, 16)
	rand.Read(token)
	key := buildKey(token, int32(2), int32(0), int32(144))

	result := validateKey(req.Context(), resp, &key)

	assert.False(t, result)

	assert.Equal(t, 400, resp.Code, "400 response is expected")
	assert.True(t, checkUploadResponse(resp.Body.Bytes(), pb.EncryptedUploadResponse_INVALID_ROLLING_START_INTERVAL_NUMBER))

	testhelpers.AssertLog(t, hook, 1, logrus.WarnLevel, "invalid rolling start number")

}

func TestValidateKey_TransmissionRiskLevelLT0(t *testing.T) {

	hook, oldLog := testhelpers.SetupTestLogging(&log)
	defer func() { log = *oldLog }()

	db := &persistence.Conn{}
	setupUploadRouter(db)

	req, _ := http.NewRequest("POST", "/upload", nil)
	resp := httptest.NewRecorder()

	//  TransmissionRiskLevel < 0
	token := make([]byte, 16)
	rand.Read(token)
	key := buildKey(token, int32(-1), int32(2651450), int32(144))

	result := validateKey(req.Context(), resp, &key)

	assert.False(t, result)

	assert.Equal(t, 400, resp.Code, "400 response is expected")
	assert.True(t, checkUploadResponse(resp.Body.Bytes(), pb.EncryptedUploadResponse_INVALID_TRANSMISSION_RISK_LEVEL))

	testhelpers.AssertLog(t, hook, 1, logrus.WarnLevel, "invalid transmission risk level")

}

func TestValidateKey_TransmissionRiskLevelGT8(t *testing.T) {

	hook, oldLog := testhelpers.SetupTestLogging(&log)
	defer func() { log = *oldLog }()

	db := &persistence.Conn{}
	setupUploadRouter(db)
	req, _ := http.NewRequest("POST", "/upload", nil)
	resp := httptest.NewRecorder()

	// Invalid TransmissionRiskLevel > 8
	token := make([]byte, 16)
	rand.Read(token)
	key := buildKey(token, int32(9), int32(2651450), int32(144))

	result := validateKey(req.Context(), resp, &key)

	assert.False(t, result)

	assert.Equal(t, 400, resp.Code, "400 response is expected")
	assert.True(t, checkUploadResponse(resp.Body.Bytes(), pb.EncryptedUploadResponse_INVALID_TRANSMISSION_RISK_LEVEL))

	testhelpers.AssertLog(t, hook, 1, logrus.WarnLevel, "invalid transmission risk level")

}

func TestValidateKey(t *testing.T) {
	_, oldLog := testhelpers.SetupTestLogging(&log)
	defer func() { log = *oldLog }()

	db := &persistence.Conn{}
	setupUploadRouter(db)

	req, _ := http.NewRequest("POST", "/upload", nil)
	resp := httptest.NewRecorder()

	// Valid key
	token := make([]byte, 16)
	rand.Read(token)
	key := buildKey(token, int32(8), int32(2651450), int32(144))

	result := validateKey(req.Context(), resp, &key)

	assert.True(t, result)
}

func TestValidateKeys(t *testing.T) {
	// Capture logs
	oldLog := log
	defer func() { log = oldLog }()

	nullLog, hook := test.NewNullLogger()
	nullLog.ExitFunc = func(code int) {}

	log = func(ctx logger.Valuer, err ...error) *logrus.Entry {
		return logrus.NewEntry(nullLog)
	}

	db := &persistence.Conn{}
	servlet := NewUploadServlet(db)
	router := Router()
	servlet.RegisterRouting(router)

	req, _ := http.NewRequest("POST", "/upload", nil)
	resp := httptest.NewRecorder()

	// Returns false on bad key
	token := make([]byte, 16)
	rand.Read(token)
	key := buildKey(token, int32(2), int32(2651450), int32(0))

	result := validateKeys(req.Context(), resp, []*pb.TemporaryExposureKey{&key})
	assert.False(t, result)

	// Returns false on keys where rsin is more than 15 days apart
	keyOne := buildKey(token, int32(2), int32(2651450), int32(144))
	keyTwo := buildKey(token, int32(2), int32(2651450-(144*15)), int32(144))

	result = validateKeys(req.Context(), resp, []*pb.TemporaryExposureKey{&keyOne, &keyTwo})
	assert.False(t, result)

	assert.Equal(t, 400, resp.Code, "400 response is expected")
	assert.True(t, checkUploadResponse(resp.Body.Bytes(), pb.EncryptedUploadResponse_INVALID_ROLLING_START_INTERVAL_NUMBER))

	testhelpers.AssertLog(t, hook, 2, logrus.WarnLevel, "sequence of rollingStartIntervalNumbers exceeds 15 days")

	// Returns true on good key
	key = buildKey(token, int32(2), int32(2651450), int32(144))

	result = validateKeys(req.Context(), resp, []*pb.TemporaryExposureKey{&key})
	assert.True(t, result)

}

func buildKey(token []byte, transmissionRiskLevel, rollingStartIntervalNumber, rollingPeriod int32) pb.TemporaryExposureKey {
	return pb.TemporaryExposureKey{
		KeyData:                    token,
		TransmissionRiskLevel:      &transmissionRiskLevel,
		RollingStartIntervalNumber: &rollingStartIntervalNumber,
		RollingPeriod:              &rollingPeriod,
	}
}

func buildUploadRequest(serverPubKey []byte, nonce []byte, appPublicKey []byte, payload []byte) *pb.EncryptedUploadRequest {
	upload := &pb.EncryptedUploadRequest{
		ServerPublicKey: serverPubKey,
		AppPublicKey:    appPublicKey,
		Nonce:           nonce,
		Payload:         payload,
	}
	return upload
}

func buildUpload(count int, ts timestamppb.Timestamp) *pb.Upload {
	var keys []*pb.TemporaryExposureKey
	for i := 0; i < count; i++ {
		keys = append(keys, randomTestKey())
	}
	upload := &pb.Upload{
		Keys:      keys,
		Timestamp: &ts,
	}
	return upload
}

func checkUploadResponse(data []byte, expectedCode pb.EncryptedUploadResponse_ErrorCode) bool {
	var response pb.EncryptedUploadResponse
	proto.Unmarshal(data, &response)
	return response.GetError() == expectedCode
}
