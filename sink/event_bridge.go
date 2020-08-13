package sink

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/eventbridge"
	log "github.com/sirupsen/logrus"
)

// EventBridge ...
type EBSink struct {
	session     *session.Session
	eventbridge *eventbridge.EventBridge
	busName     string
	stopCh      chan interface{}
	putCh       chan []byte
	batchCh     chan [][]byte
}

// New Event Bus ...
func NewEventBus() (*EBSink, error) {
	busName := os.Getenv("SINK_EVENT_BUS_NAME")

	if busName == "" {
		return nil, fmt.Errorf("[sink/eventbridge] Missing SINK_EVENT_BUS_NAME")
	}

	sess := session.Must(session.NewSession())
	svc := eventbridge.New(sess)

	req, resp := svc.DescribeEventBusRequest(&eventbridge.DescribeEventBusInput{
		Name: aws.String(busName),
	})

	fmt.Println(resp)
	fmt.Println("Eventbridge activated")

	err := req.Send()

	if err != nil {
		return nil, fmt.Errorf("Failed to find Event Bus: %s", err)
	}

	return &EBSink{
		session:     sess,
		eventbridge: svc,
		busName:     *resp.Name,
		stopCh:      make(chan interface{}),
		putCh:       make(chan []byte, 1000),
		batchCh:     make(chan [][]byte, 100),
	}, nil
}

// Start ...
func (s *EBSink) Start() error {
	fmt.Println("Eventbridge start func")
	// Stop chan for all tasks to depend on
	s.stopCh = make(chan interface{})

	go s.batch()
	go s.write()

	// wait forever for a stop signal to happen
	for {
		select {
		case <-s.stopCh:
			break
		}
		break
	}

	return nil
}

// Stop ...
func (s *EBSink) Stop() {
	fmt.Println("Eventbridge stop func")
	log.Infof("[sink/eventbridge] ensure writer queue is empty (%d messages left)", len(s.putCh))

	for len(s.putCh) > 0 {
		log.Infof("[sink/eventbridge] Waiting for queue to drain - (%d messages left)", len(s.putCh))
		time.Sleep(1 * time.Second)
	}

	close(s.stopCh)
}

// Put ..
func (s *EBSink) Put(data []byte) error {
	fmt.Println("Eventbridge put func")
	s.putCh <- data

	return nil
}

func (s *EBSink) batch() {
	fmt.Println("Eventbridge batch func")
	buffer := make([][]byte, 0)
	ticker := time.NewTicker(1 * time.Second)

	for {
		select {
		case data := <-s.putCh:
			buffer = append(buffer, data)

			if len(buffer) == 10 {
				s.batchCh <- buffer
				buffer = make([][]byte, 0)
			}

		case _ = <-ticker.C:
			// If there is anything else in the putCh, wait a little longer
			if len(s.putCh) > 0 {
				continue
			}

			if len(buffer) > 0 {
				s.batchCh <- buffer
				buffer = make([][]byte, 0)
			}
		}
	}
}

func (s *EBSink) write() {
	fmt.Println("Eventbridge write func")
	log.Infof("[sink/eventbridge] Starting writer")

	for {
		select {
		case batch := <-s.batchCh:
			entries := make([]*eventbridge.PutEventsRequestEntry, 0)

			for _, data := range batch {
				entry := &eventbridge.PutEventsRequestEntry{
					EventBusName: aws.String("michael-test"),
					Detail:       aws.String(string(data)),
					DetailType:   aws.String("Nomad Firehose"),
					Source:       aws.String("com.seatgeekadmin.NomadFirehose"),
				}

				entries = append(entries, entry)
				// id = id + 1
			}

			err := s.sendBatch(entries)
			if err != nil && strings.Contains(err.Error(), "AWS.SimpleQueueService.BatchRequestTooLong") {
				for i, el := range entries {
					err = s.sendBatch([]*eventbridge.PutEventsRequestEntry{el})
					if err != nil {
						log.Errorf("[sink/eventbridge] Retry failed for %d: %s", i, err)
					} else {
						log.Infof("[sink/eventbridge] Retry succeeded for %d", i)
					}
				}

				continue
			}

			if err != nil {
				log.Errorf("[sink/eventbridge] %s", err)
			} else {
				log.Infof("[sink/eventbridge] queued %d messages", len(batch))
			}
		}
	}
}

func (s *EBSink) sendBatch(entries []*eventbridge.PutEventsRequestEntry) error {
	fmt.Println("Eventbridge sendBatch func")
	req, _ := s.eventbridge.PutEventsRequest(&eventbridge.PutEventsInput{
		Entries: entries,
	})
	err := req.Send()
	if err == nil { // resp is now filled
		return err
	}

	return err
}
