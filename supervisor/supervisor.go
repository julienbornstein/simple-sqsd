package supervisor

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/sqs"
	"github.com/aws/aws-sdk-go/service/sqs/sqsiface"
	log "github.com/sirupsen/logrus"
)

var signature string

type Supervisor struct {
	sync.Mutex

	logger       *log.Entry
	sqs          sqsiface.SQSAPI
	httpClient   httpClient
	workerConfig WorkerConfig

	startOnce sync.Once
	wg        sync.WaitGroup

	shutdown bool
}

type WorkerConfig struct {
	QueueURL         string
	QueueMaxMessages int
	QueueWaitTime    int

	SecretKey []byte

	HTTPURL         string
	HTTPContentType string
}

type httpClient interface {
	Do(req *http.Request) (*http.Response, error)
}

func NewSupervisor(logger *log.Entry, sqs sqsiface.SQSAPI, httpClient httpClient, config WorkerConfig) *Supervisor {
	return &Supervisor{
		logger:       logger,
		sqs:          sqs,
		httpClient:   httpClient,
		workerConfig: config,
	}
}

func (s *Supervisor) Start(numWorkers int) {
	signature = "POST " + strings.TrimRight(s.workerConfig.HTTPURL, "/") + "\n"
	s.startOnce.Do(func() {
		s.wg.Add(numWorkers)

		for i := 0; i < numWorkers; i++ {
			go s.worker()
		}
	})
}

func (s *Supervisor) Wait() {
	s.wg.Wait()
}

func (s *Supervisor) Shutdown() {
	defer s.Unlock()
	s.Lock()

	s.shutdown = true
}

func (s *Supervisor) worker() {
	defer s.wg.Done()

	s.logger.Info("Starting worker")

	for {
		if s.shutdown {
			return
		}

		recInput := &sqs.ReceiveMessageInput{
			MaxNumberOfMessages: aws.Int64(int64(s.workerConfig.QueueMaxMessages)),
			QueueUrl:            aws.String(s.workerConfig.QueueURL),
			WaitTimeSeconds:     aws.Int64(int64(s.workerConfig.QueueWaitTime)),
		}

		output, err := s.sqs.ReceiveMessage(recInput)
		if err != nil {
			s.logger.Errorf("Error while receiving messages from the queue: %s", err)
			continue
		}

		if len(output.Messages) == 0 {
			continue
		}

		deleteEntries := make([]*sqs.DeleteMessageBatchRequestEntry, 0)

		for _, msg := range output.Messages {
			err := s.httpRequest(*msg.Body)
			if err != nil {
				s.logger.Errorf("Error while making HTTP request: %s", err)
				continue
			}

			deleteEntries = append(deleteEntries, &sqs.DeleteMessageBatchRequestEntry{
				Id:            msg.MessageId,
				ReceiptHandle: msg.ReceiptHandle,
			})
		}

		if len(deleteEntries) == 0 {
			continue
		}

		delInput := &sqs.DeleteMessageBatchInput{
			Entries:  deleteEntries,
			QueueUrl: aws.String(s.workerConfig.QueueURL),
		}

		_, err = s.sqs.DeleteMessageBatch(delInput)
		if err != nil {
			s.logger.Errorf("Error while deleting messages from SQS: %s", err)
		}
	}
}

func (s *Supervisor) httpRequest(body string) error {
	req, err := http.NewRequest("POST", s.workerConfig.HTTPURL, bytes.NewBufferString(body))
	if err != nil {
		return fmt.Errorf("Error while creating HTTP request: %s", err)
	}

	if len(s.workerConfig.SecretKey) > 0 {
		mac := getMac(signature+body, s.workerConfig.SecretKey)
		req.Header.Set("MAC", mac)
	}

	if len(s.workerConfig.HTTPContentType) > 0 {
		req.Header.Set("Content-Type", s.workerConfig.HTTPContentType)
	}

	res, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("Error while making HTTP request: %s", err)
	}

	res.Body.Close()

	if res.StatusCode < http.StatusOK || res.StatusCode > http.StatusIMUsed {
		return fmt.Errorf("Non-Success status code received")
	}

	return nil
}

func getMac(signature string, secretKey []byte) string {
	mac := hmac.New(sha256.New, secretKey)
	mac.Write([]byte(signature))
	return hex.EncodeToString(mac.Sum(nil))
}
