package fleetgrpc

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"sync"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	fleetv1 "github.com/Abraxas-365/odyssey/proto/fleet/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// sourceChunk is what one SourceChunk carries (well under the 4 MiB
// message limit).
const sourceChunk = 256 << 10

// FetchSource streams a wanted build's source to the agent of its cluster.
// The gateway checks the bytes against what was uploaded as it sends them:
// a stored archive that changed (or was cut) ends the stream with
// DataLoss instead of its last chunk, so the agent never builds it.
func (s *Server) FetchSource(req *fleetv1.FetchSourceRequest, stream fleetv1.Fleet_FetchSourceServer) error {
	ctx := stream.Context()
	ref := agentOf(ctx)
	id, err := fleet.ParseBuildID(req.GetBuildId())
	if err != nil {
		return status.Error(codes.InvalidArgument, "build_id is not valid")
	}
	b, err := s.agents.BuildSource(ctx, ref, id)
	if err != nil {
		return toStatus(err)
	}
	if s.sources == nil {
		return status.Error(codes.Unavailable, "this gateway serves no build sources")
	}
	if !s.fetching.acquire(ref.Cluster, s.opts.FetchesPerCluster) {
		return status.Error(codes.ResourceExhausted, "too many source downloads for this cluster at once; retry shortly")
	}
	defer s.fetching.release(ref.Cluster)
	r, err := s.sources.Open(ctx, b.SourceKey)
	if errx.IsType(err, errx.TypeNotFound) {
		return status.Error(codes.NotFound, "the build's source is gone")
	}
	if err != nil {
		return toStatus(err)
	}
	defer r.Close()

	h := sha256.New()
	buf := make([]byte, sourceChunk)
	var sent int64
	var held []byte // the last chunk waits until the whole source checked out
	for {
		n, err := io.ReadFull(r, buf)
		if n > 0 {
			if sent+int64(n) > b.SourceSize {
				return corrupt(b, "longer than uploaded")
			}
			h.Write(buf[:n])
			sent += int64(n)
			if held != nil {
				if err := stream.Send(&fleetv1.SourceChunk{Data: held}); err != nil {
					return err
				}
			}
			held = append(held[:0:0], buf[:n]...)
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return toStatus(errx.Wrap(err, "read build source", errx.TypeInternal))
		}
	}
	if sent != b.SourceSize || hex.EncodeToString(h.Sum(nil)) != b.SourceSHA256 {
		return corrupt(b, "does not match its checksum")
	}
	if held != nil {
		return stream.Send(&fleetv1.SourceChunk{Data: held})
	}
	return nil
}

func corrupt(b fleet.DesiredBuild, what string) error {
	slog.Error("stored build source "+what, "organization", b.Organization, "build", b.ID, "key", b.SourceKey)
	return status.Error(codes.DataLoss, "the build's stored source "+what)
}

// inFlight counts the source downloads of each cluster (each re-reads and
// hashes a whole archive: one cluster must not keep the gateway busy).
type inFlight struct {
	mu sync.Mutex
	n  map[fleet.ClusterID]int
}

func (f *inFlight) acquire(c fleet.ClusterID, max int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.n[c] >= max {
		return false
	}
	if f.n == nil {
		f.n = map[fleet.ClusterID]int{}
	}
	f.n[c]++
	return true
}

func (f *inFlight) release(c fleet.ClusterID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.n[c]--; f.n[c] <= 0 {
		delete(f.n, c)
	}
}
