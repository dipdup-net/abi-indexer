package grpc

import (
	"context"
	"io"
	"time"

	"github.com/dipdup-net/abi-indexer/internal/storage"
	"github.com/dipdup-net/abi-indexer/pkg/modules/grpc/pb"
	"github.com/dipdup-net/abi-indexer/pkg/modules/grpc/subscriptions"
	"github.com/pkg/errors"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc/stats"
)

const (
	successMessage = "success"
)

type contextKey string

const (
	clientID contextKey = "client_id"
)

type page struct {
	limit  uint64
	offset uint64
	order  storage.SortOrder
}

func newPage(req *pb.Page) *page {
	p := new(page)
	if req != nil {
		p.limit = req.Limit
		p.offset = req.Offset

		switch req.Order {
		case pb.SortOrder_ASC:
			p.order = storage.SortOrderAsc
		case pb.SortOrder_DESC:
			p.order = storage.SortOrderDesc
		default:
			p.order = storage.SortOrderAsc
		}
	}
	return p
}

////////////////////////////////////////////////
//////////////    HANDLERS    //////////////////
////////////////////////////////////////////////

// UnsubscribeFromHead -
func (module *Server) Hello(ctx context.Context, req *pb.HelloRequest) (*pb.HelloResponse, error) {
	id := ctx.Value(clientID)
	if id == nil {
		return nil, errors.New("unknown client")
	}

	return &pb.HelloResponse{
		Id: id.(string),
	}, nil
}

// SubscribeOnMetadata -
func (module *Server) SubscribeOnMetadata(req *pb.DefaultRequest, stream pb.MetadataService_SubscribeOnMetadataServer) error {
	var metadataSub subscriptions.Subscription[*storage.Metadata, *pb.Metadata]
	module.subsMx.Lock()
	{
		subs, err := module.getSubscriber(req.Id)
		if err != nil {
			return err
		}
		subs.Metadata = subscriptions.NewMetadata()
		metadataSub = subs.Metadata
	}
	module.subsMx.Unlock()

	for {
		select {
		case <-stream.Context().Done():
			return nil
		case msg := <-metadataSub.Listen():
			if err := stream.Send(msg); err != nil {
				if err == io.EOF {
					return nil
				}
				log.Err(err).Msg("sending message error")
			}
		}
	}
}

// UnsubscribeFromMetadata -
func (module *Server) UnsubscribeFromMetadata(ctx context.Context, req *pb.DefaultRequest) (*pb.Message, error) {
	module.subsMx.Lock()
	{
		subs, err := module.getSubscriber(req.Id)
		if err != nil {
			return nil, err
		}
		subs.Metadata = nil
	}
	module.subsMx.Unlock()

	return &pb.Message{
		Message: successMessage,
	}, nil
}

// GetMetadata -
func (module *Server) GetMetadata(ctx context.Context, req *pb.GetMetadataRequest) (*pb.Metadata, error) {
	if req == nil {
		return nil, errors.New("invalid request")
	}

	reqCtx, cancel := context.WithTimeout(ctx, time.Second*10)
	defer cancel()

	metadata, err := module.storage.Metadata.GetByAddress(reqCtx, req.Address)
	if err != nil {
		return nil, err
	}

	return Metadata(metadata), nil
}

// ListMetadata -
func (module *Server) ListMetadata(ctx context.Context, req *pb.ListMetadataRequest) (*pb.ListMetadataResponse, error) {
	p := newPage(req.GetPage())

	metadata, err := module.storage.Metadata.List(ctx, p.limit, p.offset, p.order)
	if err != nil {
		return nil, err
	}

	return ListMetadataResponse(metadata), nil
}

// GetMetadataByMethodSinature -
func (module *Server) GetMetadataByMethodSinature(ctx context.Context, req *pb.GetMetadataByMethodSinatureRequest) (*pb.ListMetadataResponse, error) {
	p := newPage(req.GetPage())

	metadata, err := module.storage.Metadata.GetByMethodSinature(ctx, req.Signature, p.limit, p.offset, p.order)
	if err != nil {
		return nil, err
	}

	return ListMetadataResponse(metadata), nil
}

// GetMetadataByTopic -
func (module *Server) GetMetadataByTopic(ctx context.Context, req *pb.GetMetadataByTopicRequest) (*pb.ListMetadataResponse, error) {
	p := newPage(req.GetPage())

	metadata, err := module.storage.Metadata.GetByTopic(ctx, req.Topic, p.limit, p.offset, p.order)
	if err != nil {
		return nil, err
	}

	return ListMetadataResponse(metadata), nil
}

func (module *Server) getSubscriber(id string) (*subscriptions.Subscriptions, error) {
	s, ok := module.subscribers[id]
	if !ok {
		return nil, errors.Errorf("unknown subscriber: %s", id)
	}
	return s, nil
}

////////////////////////////////////////////////
////////////////    STATS    ///////////////////
////////////////////////////////////////////////

// TagRPC -
func (module *Server) TagRPC(ctx context.Context, info *stats.RPCTagInfo) context.Context {
	return ctx
}

// HandleRPC -
func (module *Server) HandleRPC(ctx context.Context, s stats.RPCStats) {}

// TagConn -
func (module *Server) TagConn(ctx context.Context, info *stats.ConnTagInfo) context.Context {
	id, err := randomString(32)
	if err != nil {
		log.Err(err).Msg("invalid random string")
	}
	return context.WithValue(ctx, clientID, id)
}

// HandleConn -
func (module *Server) HandleConn(ctx context.Context, s stats.ConnStats) {
	id := ctx.Value(clientID).(string)

	switch s.(type) {
	case *stats.ConnEnd:
		module.subsMx.Lock()
		{
			if subs, ok := module.subscribers[id]; ok {
				if err := subs.Close(); err != nil {
					log.Err(err).Msg("closing subscriber")
				}
				delete(module.subscribers, id)
			}
		}
		module.subsMx.Unlock()
	case *stats.ConnBegin:
		module.subsMx.Lock()
		{
			if _, ok := module.subscribers[id]; !ok {
				module.subscribers[id] = &subscriptions.Subscriptions{
					ID: id,
				}
			}
		}
		module.subsMx.Unlock()
	}
}
