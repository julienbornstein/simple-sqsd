package main

import (
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/sqs"
	"github.com/fterrag/simple-sqsd/supervisor"
	log "github.com/sirupsen/logrus"
)

type config struct {
	AWSEndpoint string

	AppApiSecretKey []byte

	QueueRegion      string
	QueueURL         string
	QueueMaxMessages int
	QueueWaitTime    int

	HTTPMaxConns    int
	HTTPURL         string
	HTTPContentType string
}

func main() {
	c := &config{}

	c.AWSEndpoint = os.Getenv("AWS_ENDPOINT")

	c.AppApiSecretKey = []byte(os.Getenv("APP_API_SECRET_KEY"))

	c.QueueRegion = os.Getenv("SQSD_QUEUE_REGION")
	c.QueueURL = os.Getenv("SQSD_QUEUE_URL")
	c.QueueMaxMessages = getEnvInt("SQSD_QUEUE_MAX_MSGS", 10)
	c.QueueWaitTime = getEnvInt("SQSD_QUEUE_WAIT_TIME", 10)

	c.HTTPMaxConns = getEnvInt("SQSD_HTTP_MAX_CONNS", 50)
	c.HTTPURL = os.Getenv("SQSD_HTTP_URL")
	c.HTTPContentType = os.Getenv("SQSD_HTTP_CONTENT_TYPE")

	if len(c.QueueRegion) == 0 {
		log.Fatal("SQSD_QUEUE_REGION cannot be empty")
	}

	if len(c.QueueURL) == 0 {
		log.Fatal("SQSD_QUEUE_URL cannot be empty")
	}

	if len(c.HTTPURL) == 0 {
		log.Fatal("SQSD_HTTP_URL cannot be empty")
	}

	log.SetFormatter(&log.JSONFormatter{})
	logger := log.WithFields(log.Fields{
		"queueRegion":  c.QueueRegion,
		"queueUrl":     c.QueueURL,
		"httpMaxConns": c.HTTPMaxConns,
		"httpPath":     c.HTTPURL,
	})

	httpClient := &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        c.HTTPMaxConns,
			MaxIdleConnsPerHost: c.HTTPMaxConns,
		},
	}

	awsSess := session.Must(session.NewSessionWithOptions(session.Options{
		SharedConfigState: session.SharedConfigEnable,
	}))

	sqsConfig := aws.NewConfig().
		WithRegion(c.QueueRegion).
		WithHTTPClient(httpClient)

	if c.AWSEndpoint != "" {
		sqsConfig.WithEndpoint(c.AWSEndpoint)
	}

	sqsSvc := sqs.New(awsSess, sqsConfig)

	// To workaround a kube2iam issue, expire credentials every minute.
	go func() {
		for {
			sqsSvc.Config.Credentials.Expire()
			time.Sleep(time.Minute)
		}
	}()

	wConf := supervisor.WorkerConfig{
		QueueURL:         c.QueueURL,
		QueueMaxMessages: c.QueueMaxMessages,
		QueueWaitTime:    c.QueueWaitTime,

		SecretKey: c.AppApiSecretKey,

		HTTPURL:         c.HTTPURL,
		HTTPContentType: c.HTTPContentType,
	}

	s := supervisor.NewSupervisor(logger, sqsSvc, httpClient, wConf)
	s.Start(c.HTTPMaxConns)
	s.Wait()
}

func getEnvInt(key string, def int) int {
	val, err := strconv.Atoi(os.Getenv(key))
	if err != nil {
		return def
	}

	return val
}
