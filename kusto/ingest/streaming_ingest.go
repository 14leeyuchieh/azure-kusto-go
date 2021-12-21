package ingest

import (
	"context"
	"io"
	"io/ioutil"
	"os"

	"github.com/Azure/azure-kusto-go/kusto"
	"github.com/Azure/azure-kusto-go/kusto/data/errors"
	"github.com/Azure/azure-kusto-go/kusto/ingest/internal/conn"
	"github.com/Azure/azure-kusto-go/kusto/ingest/internal/filesystem"
	"github.com/Azure/azure-kusto-go/kusto/ingest/internal/gzip"
	"github.com/Azure/azure-kusto-go/kusto/ingest/internal/properties"
)

// StreamingIngestion provides data ingestion from external sources into Kusto.
type StreamingIngestion struct {
	db    string
	table string

	client *kusto.Client

	streamConn *conn.Conn
}

// NewStreaming is the constructor for StreamingIngestion.
func NewStreaming(client *kusto.Client, db, table string) (*StreamingIngestion, error) {
	streamConn, err := conn.New(client.Endpoint(), client.Auth())
	if err != nil {
		return nil, err
	}

	i := &StreamingIngestion{
		db:         db,
		table:      table,
		client:     client,
		streamConn: streamConn,
	}

	return i, nil
}

// FromFile allows uploading a data file for Kusto from either a local path or a blobstore URI path.
// This method is thread-safe.
func (i *StreamingIngestion) FromFile(ctx context.Context, fPath string, options ...FileOption) (*Result, error) {
	local, err := filesystem.IsLocalPath(fPath)
	if err != nil {
		return nil, err
	}

	if !local {
		return nil, errors.ES(errors.OpFileIngest, errors.KClientArgs, "blobstore paths are not supported for streaming")
	}
	props := i.newProp()

	for _, option := range options {
		err := option.Run(&props, true, false, false, false, true)
		if err != nil {
			return nil, err
		}
	}

	discovery := filesystem.CompressionDiscovery(fPath)
	if discovery != properties.CTUnknown && discovery != properties.CTNone {
		props.Streaming.ShouldCompress = true
	}

	file, err := os.Open(fPath)
	if err != nil {
		return nil, err
	}

	return streamImpl(i.db, i.table, i.streamConn, ctx, file, props)
}

// FromReader allows uploading a data file for Kusto from an io.Reader. The content is uploaded to Blobstore and
// ingested after all data in the reader is processed. Content should not use compression as the content will be
// compressed with gzip. This method is thread-safe.
func (i *StreamingIngestion) FromReader(ctx context.Context, reader io.Reader, options ...FileOption) (*Result, error) {
	props := i.newProp()

	for _, prop := range options {
		err := prop.Run(&props, false, false, true, false, true)
		if err != nil {
			return nil, err
		}
	}

	props.Streaming.ShouldCompress = true

	return streamImpl(i.db, i.table, i.streamConn, ctx, reader, props)
}

func streamImpl(db, table string, c *conn.Conn, ctx context.Context, payload io.Reader, props properties.All) (*Result, error) {
	if props.Streaming.ShouldCompress {
		var closer io.ReadCloser
		var ok bool
		if closer, ok = payload.(io.ReadCloser); !ok {
			closer = ioutil.NopCloser(payload)
		}
		zw := gzip.New()
		zw.Reset(closer)

		payload = zw
	}

	err := c.Write(ctx, db, table, payload, props.Ingestion.Additional.Format, props.Ingestion.Additional.IngestionMappingRef, props.Streaming.ClientRequestId)

	if err != nil {
		return nil, errors.E(errors.OpIngestStream, errors.KClientArgs, err)
	}

	result := newResult()
	result.putProps(props)
	result.record.Status = "Success"

	return result, nil
}

func (i *StreamingIngestion) newProp() properties.All {
	return properties.All{
		Ingestion: properties.Ingestion{
			DatabaseName: i.db,
			TableName:    i.table,
		},
	}
}
