package main

import (
	"encoding/json"
	"errors"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"
)

//TODO:  This needs connection handling logic added. Currently it's pretty rudimentary on failures

//NatsConn struct to satisfy the interface
type NatsConn struct {
	Conn *nats.Conn
	JS   nats.JetStreamContext
}

//Connect to the NATS message queue
func (natsConn *NatsConn) Connect(host, port string, errChan chan error) {
	log.Info().Msgf("Connecting to NATS: %v:%v", host, port)
	nh := "nats://" + host + ":" + port
	conn, err := nats.Connect(nh,
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			errChan <- err
		}),
		nats.DisconnectHandler(func(_ *nats.Conn) {
			errChan <- errors.New("unexpectedly disconnected from nats")
		}),
	)
	if err != nil {
		errChan <- err
		return
	}
	log.Debug().Msg("setting up nats connection")
	natsConn.Conn = conn

	log.Debug().Msg("setting up jetstream")
	natsConn.JS, err = conn.JetStream()
	if err != nil {
		log.Debug().Msg("error connecting to jetstream")
		errChan <- err
		return
	}
	log.Debug().Msg("creating streams")
	err = natsConn.createStream()
	if err != nil {
		log.Debug().Msg("error creating stream")
		errChan <- err
		return
	}
	log.Info().Msg("successfully connected to nats")
}

//Publish push messages to NATS
func (natsConn *NatsConn) Publish(run *Run) error {
	data, err := json.Marshal(run)
	if err != nil {
		return err
	}
	log.Debug().RawJSON("results-topic", data)
	//log.Debug().Msgf("Publishing scan: %v to topic: %v", string(data), publish)
	_, err = natsConn.JS.Publish(publish, data)
	if err != nil {
		return err
	}
	return nil
}

/*
 * TODO: There's a bug here where a message needs to be acked back after a scan is finished
 */
//Subscribe subscribe to a topic in NATS
func (natsConn *NatsConn) Subscribe(workers int, errChan chan error) chan *Message {
	log.Info().Msgf("Listening on topic: %v", subscription)
	bch := make(chan *Message, 1)
	sub, err := natsConn.JS.PullSubscribe(subscription, durableName, nats.PullMaxWaiting(128), nats.ManualAck())
	if err != nil {
		errChan <- err
		return nil
	}
	go func() {
		for {
			msgs, err := sub.Fetch(workers, nats.MaxWait(10*time.Second))
			for _, msg := range msgs {
				log.Debug().Msgf("got message: %f", msg)
				if err != nil {
					errChan <- err
				}
				message := newMessage(msg.Data)
				bch <- message
				ack := message.Processed()
				if !ack {
					msg.Nak()
					continue
				}
				msg.Ack()
			}
		}
	}()
	return bch
}

//Close the connection
func (natsConn *NatsConn) Close() {
	natsConn.Conn.Drain()
}

//Setup the streams
func (natsConn *NatsConn) createStream() error {
	log.Debug().Msg("starting create stream function")
	stream, err := natsConn.JS.StreamInfo(streamName)
	log.Debug().Msg("stream created")
	if err != nil {
		log.Error().Err(err)
	}
	log.Debug().Msg("setting up stream config")
	natsConfig := &nats.StreamConfig{
		Name:     streamName,
		Subjects: []string{subscription},
	}
	if stream == nil {
		log.Info().Msgf("creating stream %s", subscription)
		_, err := natsConn.JS.AddStream(natsConfig)
		log.Debug().Msg("stream created")
		if err != nil {
			return err
		}
	}
	return nil
}
