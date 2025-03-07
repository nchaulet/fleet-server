// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/elastic/fleet-server/v7/internal/pkg/bulk"
	"github.com/elastic/fleet-server/v7/internal/pkg/cache"
	"github.com/elastic/fleet-server/v7/internal/pkg/config"
	"github.com/elastic/fleet-server/v7/internal/pkg/dl"
	"github.com/elastic/fleet-server/v7/internal/pkg/logger"
	"github.com/elastic/fleet-server/v7/internal/pkg/model"
	"github.com/elastic/fleet-server/v7/internal/pkg/throttle"

	"github.com/julienschmidt/httprouter"
	"github.com/pkg/errors"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

const (
	defaultMaxParallel = 8           // TODO: configurable
	defaultThrottleTTL = time.Minute // TODO: configurable
)

var (
	ErrorThrottle     = errors.New("cannot acquire throttle token")
	ErrorBadSha2      = errors.New("malformed sha256")
	ErrorRecord       = errors.New("artifact record mismatch")
	ErrorMismatchSha2 = errors.New("mismatched sha256")
)

type ArtifactT struct {
	bulker     bulk.Bulk
	cache      cache.Cache
	esThrottle *throttle.Throttle
}

func NewArtifactT(cfg *config.Server, bulker bulk.Bulk, cache cache.Cache) *ArtifactT {
	return &ArtifactT{
		bulker:     bulker,
		cache:      cache,
		esThrottle: throttle.NewThrottle(defaultMaxParallel),
	}
}

func (rt *Router) handleArtifacts(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	start := time.Now()

	var (
		id   = ps.ByName("id")   // Identifier in the artifact record
		sha2 = ps.ByName("sha2") // DecodedSha256 in the artifact record
	)

	reqID := r.Header.Get(logger.HeaderRequestID)

	zlog := log.With().
		Str(LogAgentID, id).
		Str(ECSHTTPRequestID, reqID).
		Str("sha2", sha2).
		Str("remoteAddr", r.RemoteAddr).
		Logger()

	rdr, err := rt.at.handleArtifacts(&zlog, r, id, sha2)

	var nWritten int64
	if err == nil {
		nWritten, err = io.Copy(w, rdr)
		zlog.Trace().
			Err(err).
			Int64(ECSHTTPResponseBodyBytes, nWritten).
			Int64(ECSEventDuration, time.Since(start).Nanoseconds()).
			Msg("Response sent")

		cntArtifacts.bodyOut.Add(uint64(nWritten))
	}

	if err != nil {
		cntArtifacts.IncError(err)
		resp := NewHTTPErrResp(err)

		zlog.WithLevel(resp.Level).
			Err(err).
			Int(ECSHTTPResponseCode, resp.StatusCode).
			Int64(ECSHTTPResponseBodyBytes, nWritten).
			Int64(ECSEventDuration, time.Since(start).Nanoseconds()).
			Msg("fail artifact")

		if err := resp.Write(w); err != nil {
			zlog.Error().Err(err).Msg("fail writing error response")
		}
	}
}

func (at ArtifactT) handleArtifacts(zlog *zerolog.Logger, r *http.Request, id, sha2 string) (io.Reader, error) {
	// Authenticate the APIKey; retrieve agent record.
	// Note: This is going to be a bit slow even if we hit the cache on the api key.
	// In order to validate that the agent still has that api key, we fetch the agent record from elastic.
	agent, err := authAgent(r, nil, at.bulker, at.cache)
	if err != nil {
		return nil, err
	}

	// Pointer is passed in to allow UpdateContext by child function
	zlog.UpdateContext(func(ctx zerolog.Context) zerolog.Context {
		return ctx.Str(LogAccessAPIKeyID, agent.AccessAPIKeyID)
	})

	return at.processRequest(r.Context(), *zlog, agent, id, sha2)
}

func (at ArtifactT) processRequest(ctx context.Context, zlog zerolog.Logger, agent *model.Agent, id, sha2 string) (io.Reader, error) {

	// Input validation
	if err := validateSha2String(sha2); err != nil {
		return nil, err
	}

	// Determine whether the agent should have access to this artifact
	if err := at.authorizeArtifact(ctx, agent, id, sha2); err != nil {
		zlog.Warn().Err(err).Msg("Unauthorized GET on artifact")
		return nil, err
	}

	// Grab artifact, whether from cache or elastic.
	artifact, err := at.getArtifact(ctx, zlog, id, sha2)
	if err != nil {
		return nil, err
	}

	// Sanity check; just in case something underneath is misbehaving
	if artifact.Identifier != id || artifact.DecodedSha256 != sha2 {
		err = ErrorRecord
		zlog.Info().
			Err(err).
			Str("artifact_id", artifact.Identifier).
			Str("artifact_sha2", artifact.DecodedSha256).
			Msg("Identifier mismatch on url")
		return nil, err
	}

	zlog.Debug().
		Int("sz", len(artifact.Body)).
		Int64("decodedSz", artifact.DecodedSize).
		Str("compression", artifact.CompressionAlgorithm).
		Str("encryption", artifact.EncryptionAlgorithm).
		Str("created", artifact.Created).
		Msg("Artifact GET")

	// Write the payload
	rdr := bytes.NewReader(artifact.Body)
	return rdr, nil
}

// TODO: Pull the policy record for this agent and validate that the
// requested artifact is assigned to this policy.  This will prevent
// agents from retrieving artifacts that they do not have access to.
// Note that this is racy, the policy could have changed to allow an
// artifact before this instantiation of FleetServer has its local
// copy updated.  Take the race conditions into consideration.
//
// Initial implementation is dependent on security by obscurity; ie.
// it should be difficult for an attacker to guess a guid.
func (at ArtifactT) authorizeArtifact(ctx context.Context, agent *model.Agent, ident, sha2 string) error {
	return nil // TODO
}

// Return artifact from cache by sha2 or fetch directly from Elastic.
// Update cache on successful retrieval from Elastic.
func (at ArtifactT) getArtifact(ctx context.Context, zlog zerolog.Logger, ident, sha2 string) (*model.Artifact, error) {

	// Check the cache; return immediately if found.
	if artifact, ok := at.cache.GetArtifact(ident, sha2); ok {
		return &artifact, nil
	}

	// Fetch the artifact from elastic
	art, err := at.fetchArtifact(ctx, zlog, ident, sha2)

	if err != nil {
		zlog.Info().Err(err).Msg("Fail retrieve artifact")
		return nil, err
	}

	// The 'Body' field type is Raw; extract to string.
	var srcPayload string
	if err = json.Unmarshal(art.Body, &srcPayload); err != nil {
		zlog.Error().Err(err).Msg("Cannot unmarshal artifact payload")
		return nil, err
	}

	// Artifact is stored base64 encoded in ElasticSearch.
	// Base64 decode the payload before putting in cache
	// to avoid having to decode on each cache hit.
	dstPayload, err := base64.StdEncoding.DecodeString(srcPayload)
	if err != nil {
		zlog.Error().Err(err).Msg("Fail base64 decode artifact")
		return nil, err
	}

	// Validate the sha256 hash; this is just good hygiene.
	if err = validateSha2Data(dstPayload, art.EncodedSha256); err != nil {
		zlog.Error().Err(err).Msg("Fail sha2 hash validation")
		return nil, err
	}

	// Reassign decoded payload before adding to cache, avoid base64 decode on cache hit.
	art.Body = dstPayload

	// Update the cache.
	at.cache.SetArtifact(*art)

	return art, nil
}

// Attempt to fetch the artifact from Elastic
// TODO: Design a mechanism to mitigate a DDOS attack on bogus hashes.
// Perhaps have a cache of the most recently used hashes available, and items that aren't
// in the cache can do a lookup but throttle as below.  We could update the cache every 10m or so.
func (at ArtifactT) fetchArtifact(ctx context.Context, zlog zerolog.Logger, ident, sha2 string) (*model.Artifact, error) {
	// Throttle prevents more than N outstanding requests to elastic globally and per sha2.
	if token := at.esThrottle.Acquire(sha2, defaultThrottleTTL); token == nil {
		return nil, ErrorThrottle
	} else {
		defer token.Release()
	}

	start := time.Now()
	artifact, err := dl.FindArtifact(ctx, at.bulker, ident, sha2)

	zlog.Info().
		Err(err).
		Int64(ECSEventDuration, time.Since(start).Nanoseconds()).
		Msg("fetch artifact")

	return artifact, errors.Wrap(err, "fetchArtifact")
}

func validateSha2String(sha2 string) error {

	if len(sha2) != 64 {
		return ErrorBadSha2
	}

	if _, err := hex.DecodeString(sha2); err != nil {
		return ErrorBadSha2
	}

	return nil
}

func validateSha2Data(data []byte, sha2 string) error {
	src, err := hex.DecodeString(sha2)
	if err != nil {
		return errors.Wrap(err, "sha2 hex decode")
	}

	sum := sha256.Sum256(data)
	if !bytes.Equal(sum[:], src) {
		return ErrorMismatchSha2
	}

	return nil
}
