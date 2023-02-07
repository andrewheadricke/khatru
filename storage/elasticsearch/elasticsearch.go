package elasticsearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/elastic/go-elasticsearch/v8/esutil"
	"github.com/nbd-wtf/go-nostr"
)

var indexMapping = `
{
	"settings": {
		"number_of_shards": 1,
		"number_of_replicas": 0
	},
	"mappings": {
		"dynamic": false,
		"properties": {
			"id": {"type": "keyword"},
			"pubkey": {"type": "keyword"},
			"kind": {"type": "integer"},
			"tags": {"type": "keyword"},
			"created_at": {"type": "date"}
		}
	}
}
`

type ElasticsearchStorage struct {
	es        *elasticsearch.Client
	bi        esutil.BulkIndexer
	indexName string
}

func (ess *ElasticsearchStorage) Init() error {
	es, err := elasticsearch.NewDefaultClient()
	if err != nil {
		return err
	}
	// log.Println(elasticsearch.Version)
	// log.Println(es.Info())

	// todo: config
	ess.indexName = "test"

	// todo: don't delete index every time
	// es.Indices.Delete([]string{ess.indexName})

	res, err := es.Indices.Create(ess.indexName, es.Indices.Create.WithBody(strings.NewReader(indexMapping)))
	if err != nil {
		return err
	}
	if res.IsError() {
		body, _ := io.ReadAll(res.Body)
		txt := string(body)
		if !strings.Contains(txt, "resource_already_exists_exception") {
			return fmt.Errorf("%s", txt)
		}
	}

	// bulk indexer
	bi, err := esutil.NewBulkIndexer(esutil.BulkIndexerConfig{
		Index:         ess.indexName,   // The default index name
		Client:        es,              // The Elasticsearch client
		NumWorkers:    2,               // The number of worker goroutines
		FlushInterval: 3 * time.Second, // The periodic flush interval
	})
	if err != nil {
		log.Fatalf("Error creating the indexer: %s", err)
	}

	ess.es = es
	ess.bi = bi

	return nil
}

func (ess *ElasticsearchStorage) DeleteEvent(id string, pubkey string) error {
	// todo: is pubkey match required?

	done := make(chan error)
	err := ess.bi.Add(
		context.Background(),
		esutil.BulkIndexerItem{
			Action:     "delete",
			DocumentID: id,
			OnSuccess: func(ctx context.Context, item esutil.BulkIndexerItem, res esutil.BulkIndexerResponseItem) {
				close(done)
			},
			OnFailure: func(ctx context.Context, item esutil.BulkIndexerItem, res esutil.BulkIndexerResponseItem, err error) {
				if err != nil {
					done <- err
				} else {
					// ok if deleted item not found
					if res.Status == 404 {
						close(done)
						return
					}
					txt, _ := json.Marshal(res)
					err := fmt.Errorf("ERROR: %s", txt)
					done <- err
				}
			},
		},
	)
	if err != nil {
		return err
	}

	err = <-done
	if err != nil {
		log.Println("DEL", err)
	}
	return err
}

func (ess *ElasticsearchStorage) SaveEvent(event *nostr.Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}

	done := make(chan error)

	// adapted from:
	// https://github.com/elastic/go-elasticsearch/blob/main/_examples/bulk/indexer.go#L196
	err = ess.bi.Add(
		context.Background(),
		esutil.BulkIndexerItem{
			Action:     "index",
			DocumentID: event.ID,
			Body:       bytes.NewReader(data),
			OnSuccess: func(ctx context.Context, item esutil.BulkIndexerItem, res esutil.BulkIndexerResponseItem) {
				close(done)
			},
			OnFailure: func(ctx context.Context, item esutil.BulkIndexerItem, res esutil.BulkIndexerResponseItem, err error) {
				if err != nil {
					done <- err
				} else {
					err := fmt.Errorf("ERROR: %s: %s", res.Error.Type, res.Error.Reason)
					done <- err
				}
			},
		},
	)
	if err != nil {
		return err
	}

	err = <-done
	if err != nil {
		log.Println("SAVE", err)
	}
	return err
}
