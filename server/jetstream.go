// Copyright 2019-2020 The NATS Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/minio/highwayhash"
	"github.com/nats-io/nats-server/v2/server/sysmem"
)

// JetStreamConfig determines this server's configuration.
// MaxMemory and MaxStore are in bytes.
type JetStreamConfig struct {
	MaxMemory int64
	MaxStore  int64
	StoreDir  string
}

// TODO(dlc) - need to track and rollup against server limits, etc.
type JetStreamAccountLimits struct {
	MaxMemory    int64 `json:"max_memory"`
	MaxStore     int64 `json:"max_storage"`
	MaxStreams   int   `json:"max_streams"`
	MaxConsumers int   `json:"max_consumers"`
}

// JetStreamAccountStats returns current statistics about the account's JetStream usage.
type JetStreamAccountStats struct {
	Memory  uint64                 `json:"memory"`
	Store   uint64                 `json:"storage"`
	Streams int                    `json:"streams"`
	Limits  JetStreamAccountLimits `json:"limits"`
}

// Responses to requests sent to a server from a client.
const (
	// OK response
	OK = "+OK"
	// ERR prefix response
	ErrPrefix = "-ERR"

	// JetStreamNotEnabled is returned when JetStream is not enabled.
	JetStreamNotEnabled = "-ERR 'jetstream not enabled for account'"
	// JetStreamBadRequest is returned when the request could not be properly parsed.
	JetStreamBadRequest = "-ERR 'bad request'"
)

// Request API subjects for JetStream.
const (
	// JetStreamEnabled allows a user to dynamically check if JetStream is enabled for an account.
	// Will return +OK on success, otherwise will timeout.
	JetStreamEnabled = "$JS.ENABLED"

	// JetStreamInfo is for obtaining general information about JetStream for this account.
	// Will return JSON response.
	JetStreamInfo = "$JS.INFO"

	// JetStreamCreateTemplate is the endpoint to create new stream templates.
	// Will return +OK on success and -ERR on failure.
	JetStreamCreateTemplate  = "$JS.TEMPLATE.*.CREATE"
	JetStreamCreateTemplateT = "$JS.TEMPLATE.%s.CREATE"

	// JetStreamListTemplates is the endpoint to list all stream templates for this account.
	// Will return json list of string on success and -ERR on failure.
	JetStreamListTemplates = "$JS.TEMPLATES.LIST"

	// JetStreamTemplateInfo is for obtaining general information about a named stream template.
	// Will return JSON response.
	JetStreamTemplateInfo  = "$JS.TEMPLATE.*.INFO"
	JetStreamTemplateInfoT = "$JS.TEMPLATE.%s.INFO"

	// JetStreamDeleteTemplate is the endpoint to delete stream templates.
	// Will return +OK on success and -ERR on failure.
	JetStreamDeleteTemplate  = "$JS.TEMPLATE.*.DELETE"
	JetStreamDeleteTemplateT = "$JS.TEMPLATE.%s.DELETE"

	// JetStreamCreateStream is the endpoint to create new streams.
	// Will return +OK on success and -ERR on failure.
	JetStreamCreateStream  = "$JS.STREAM.*.CREATE"
	JetStreamCreateStreamT = "$JS.STREAM.%s.CREATE"

	// JetStreamListStreams is the endpoint to list all streams for this account.
	// Will return json list of string on success and -ERR on failure.
	JetStreamListStreams = "$JS.STREAM.LIST"

	// JetStreamStreamInfo is for obtaining general information about a named stream.
	// Will return JSON response.
	JetStreamStreamInfo  = "$JS.STREAM.*.INFO"
	JetStreamStreamInfoT = "$JS.STREAM.%s.INFO"

	// JetStreamDeleteStream is the endpoint to delete streams.
	// Will return +OK on success and -ERR on failure.
	JetStreamDeleteStream  = "$JS.STREAM.*.DELETE"
	JetStreamDeleteStreamT = "$JS.STREAM.%s.DELETE"

	// JetStreamPurgeStream is the endpoint to purge streams.
	// Will return +OK on success and -ERR on failure.
	JetStreamPurgeStream  = "$JS.STREAM.*.PURGE"
	JetStreamPurgeStreamT = "$JS.STREAM.%s.PURGE"

	// JetStreamDeleteMsg is the endpoint to delete messages from a stream.
	// Will return +OK on success and -ERR on failure.
	JetStreamDeleteMsg  = "$JS.STREAM.*.MSG.DELETE"
	JetStreamDeleteMsgT = "$JS.STREAM.%s.MSG.DELETE"

	// JetStreamCreateConsumer is the endpoint to create durable consumers for streams.
	// You need to include the stream and consumer name in the subject.
	// Will return +OK on success and -ERR on failure.
	JetStreamCreateConsumer  = "$JS.STREAM.*.CONSUMER.*.CREATE"
	JetStreamCreateConsumerT = "$JS.STREAM.%s.CONSUMER.%s.CREATE"

	// JetStreamCreateEphemeralConsumer is the endpoint to create ephemeral consumers for streams.
	// Will return +OK <consumer name> on success and -ERR on failure.
	JetStreamCreateEphemeralConsumer  = "$JS.STREAM.*.EPHEMERAL.CONSUMER.CREATE"
	JetStreamCreateEphemeralConsumerT = "$JS.STREAM.%s.EPHEMERAL.CONSUMER.CREATE"

	// JetStreamConsumers is the endpoint to list all consumers for the stream.
	// Will return json list of string on success and -ERR on failure.
	JetStreamConsumers  = "$JS.STREAM.*.CONSUMERS"
	JetStreamConsumersT = "$JS.STREAM.%s.CONSUMERS"

	// JetStreamConsumerInfo is for obtaining general information about a consumer.
	// Will return JSON response.
	JetStreamConsumerInfo  = "$JS.STREAM.*.CONSUMER.*.INFO"
	JetStreamConsumerInfoT = "$JS.STREAM.%s.CONSUMER.%s.INFO"

	// JetStreamDeleteConsumer is the endpoint to delete consumers.
	// Will return +OK on success and -ERR on failure.
	JetStreamDeleteConsumer  = "$JS.STREAM.*.CONSUMER.*.DELETE"
	JetStreamDeleteConsumerT = "$JS.STREAM.%s.CONSUMER.%s.DELETE"

	// JetStreamAckT is the template for the ack message stream coming back from an consumer
	// when they ACK/NAK, etc a message.
	JetStreamAckT = "$JS.ACK.%s.%s"

	// JetStreamRequestNextT is the prefix for the request next message(s) for a consumer in worker/pull mode.
	JetStreamRequestNextT = "$JS.STREAM.%s.CONSUMER.%s.NEXT"

	// JetStreamMsgBySeqT is the template for direct requests for a message by its stream sequence number.
	JetStreamMsgBySeqT = "$JS.STREAM.%s.MSG.BYSEQ"

	// JetStreamAdvisoryPrefix is a prefix for all JetStream advisories
	JetStreamAdvisoryPrefix = "$JS.EVENT.ADVISORY"

	// JetStreamMetricPrefix is a prefix for all JetStream metrics
	JetStreamMetricPrefix = "$JS.EVENT.METRIC"

	// JetStreamMetricConsumerAckPre is a metric containing ack latency
	JetStreamMetricConsumerAckPre = JetStreamMetricPrefix + ".CONSUMER_ACK"

	// JetStreamAdvisoryConsumerMaxDeliveryExceedPre is a notification published when a message exceeds its delivery threshold
	JetStreamAdvisoryConsumerMaxDeliveryExceedPre = JetStreamAdvisoryPrefix + ".MAX_DELIVERIES"
)

// This is for internal accounting for JetStream for this server.
type jetStream struct {
	mu            sync.RWMutex
	srv           *Server
	config        JetStreamConfig
	accounts      map[*Account]*jsAccount
	memReserved   int64
	storeReserved int64
}

// This represents a jetstream  enabled account.
// Worth noting that we include the js ptr, this is because
// in general we want to be very efficient when receiving messages on
// and internal sub for a msgSet, so we will direct link to the msgSet
// and walk backwards as needed vs multiple hash lookups and locks, etc.
type jsAccount struct {
	mu            sync.RWMutex
	js            *jetStream
	account       *Account
	limits        JetStreamAccountLimits
	memReserved   int64
	memUsed       int64
	storeReserved int64
	storeUsed     int64
	storeDir      string
	streams       map[string]*Stream
	templates     map[string]*StreamTemplate
	store         TemplateStore
}

// For easier handling of exports and imports.
var allJsExports = []string{
	JetStreamEnabled,
	JetStreamInfo,
	JetStreamCreateTemplate,
	JetStreamListTemplates,
	JetStreamTemplateInfo,
	JetStreamDeleteTemplate,
	JetStreamCreateStream,
	JetStreamListStreams,
	JetStreamStreamInfo,
	JetStreamDeleteStream,
	JetStreamPurgeStream,
	JetStreamDeleteMsg,
	JetStreamCreateConsumer,
	JetStreamCreateEphemeralConsumer,
	JetStreamConsumers,
	JetStreamConsumerInfo,
	JetStreamDeleteConsumer,
}

// EnableJetStream will enable JetStream support on this server with the given configuration.
// A nil configuration will dynamically choose the limits and temporary file storage directory.
// If this server is part of a cluster, a system account will need to be defined.
func (s *Server) EnableJetStream(config *JetStreamConfig) error {
	s.mu.Lock()
	if !s.standAloneMode() {
		s.mu.Unlock()
		return fmt.Errorf("jetstream restricted to single server mode for now")
	}
	if s.js != nil {
		s.mu.Unlock()
		return fmt.Errorf("jetstream already enabled")
	}
	s.Noticef("Starting JetStream")
	if config == nil || config.MaxMemory <= 0 || config.MaxStore <= 0 {
		var storeDir string
		s.Debugf("JetStream creating dynamic configuration - 75%% of system memory, %s disk", FriendlyBytes(JetStreamMaxStoreDefault))
		if config != nil {
			storeDir = config.StoreDir
		}
		config = s.dynJetStreamConfig(storeDir)
	}
	// Copy, don't change callers.
	cfg := *config
	if cfg.StoreDir == "" {
		cfg.StoreDir = filepath.Join(os.TempDir(), JetStreamStoreDir)
	}

	s.js = &jetStream{srv: s, config: cfg, accounts: make(map[*Account]*jsAccount)}
	s.mu.Unlock()

	// FIXME(dlc) - Allow memory only operation?
	if stat, err := os.Stat(cfg.StoreDir); os.IsNotExist(err) {
		if err := os.MkdirAll(cfg.StoreDir, 0755); err != nil {
			return fmt.Errorf("could not create storage directory - %v", err)
		}
	} else {
		// Make sure its a directory and that we can write to it.
		if stat == nil || !stat.IsDir() {
			return fmt.Errorf("storage directory is not a directory")
		}
		tmpfile, err := ioutil.TempFile(cfg.StoreDir, "_test_")
		if err != nil {
			return fmt.Errorf("storage directory is not writable")
		}
		os.Remove(tmpfile.Name())
	}

	// JetStream is an internal service so we need to make sure we have a system account.
	// This system account will export the JetStream service endpoints.
	if sacc := s.SystemAccount(); sacc == nil {
		s.SetDefaultSystemAccount()
	}

	// Setup our internal subscriptions.
	if _, err := s.sysSubscribe(JetStreamEnabled, s.isJsEnabledRequest); err != nil {
		return fmt.Errorf("Error setting up internal jetstream subscriptions: %v", err)
	}
	if _, err := s.sysSubscribe(JetStreamInfo, s.jsAccountInfoRequest); err != nil {
		return fmt.Errorf("Error setting up internal jetstream subscriptions: %v", err)
	}
	if _, err := s.sysSubscribe(JetStreamCreateTemplate, s.jsCreateTemplateRequest); err != nil {
		return fmt.Errorf("Error setting up internal jetstream subscriptions: %v", err)
	}
	if _, err := s.sysSubscribe(JetStreamListTemplates, s.jsTemplateListRequest); err != nil {
		return fmt.Errorf("Error setting up internal jetstream subscriptions: %v", err)
	}
	if _, err := s.sysSubscribe(JetStreamTemplateInfo, s.jsTemplateInfoRequest); err != nil {
		return fmt.Errorf("Error setting up internal jetstream subscriptions: %v", err)
	}
	if _, err := s.sysSubscribe(JetStreamDeleteTemplate, s.jsTemplateDeleteRequest); err != nil {
		return fmt.Errorf("Error setting up internal jetstream subscriptions: %v", err)
	}
	if _, err := s.sysSubscribe(JetStreamCreateStream, s.jsCreateStreamRequest); err != nil {
		return fmt.Errorf("Error setting up internal jetstream subscriptions: %v", err)
	}
	if _, err := s.sysSubscribe(JetStreamListStreams, s.jsStreamListRequest); err != nil {
		return fmt.Errorf("Error setting up internal jetstream subscriptions: %v", err)
	}
	if _, err := s.sysSubscribe(JetStreamStreamInfo, s.jsStreamInfoRequest); err != nil {
		return fmt.Errorf("Error setting up internal jetstream subscriptions: %v", err)
	}
	if _, err := s.sysSubscribe(JetStreamDeleteStream, s.jsStreamDeleteRequest); err != nil {
		return fmt.Errorf("Error setting up internal jetstream subscriptions: %v", err)
	}
	if _, err := s.sysSubscribe(JetStreamPurgeStream, s.jsStreamPurgeRequest); err != nil {
		return fmt.Errorf("Error setting up internal jetstream subscriptions: %v", err)
	}
	if _, err := s.sysSubscribe(JetStreamDeleteMsg, s.jsMsgDeleteRequest); err != nil {
		return fmt.Errorf("Error setting up internal jetstream subscriptions: %v", err)
	}
	if _, err := s.sysSubscribe(JetStreamCreateConsumer, s.jsCreateConsumerRequest); err != nil {
		return fmt.Errorf("Error setting up internal jetstream subscriptions: %v", err)
	}
	if _, err := s.sysSubscribe(JetStreamCreateEphemeralConsumer, s.jsCreateEphemeralConsumerRequest); err != nil {
		return fmt.Errorf("Error setting up internal jetstream subscriptions: %v", err)
	}
	if _, err := s.sysSubscribe(JetStreamConsumers, s.jsConsumersRequest); err != nil {
		return fmt.Errorf("Error setting up internal jetstream subscriptions: %v", err)
	}
	if _, err := s.sysSubscribe(JetStreamConsumerInfo, s.jsConsumerInfoRequest); err != nil {
		return fmt.Errorf("Error setting up internal jetstream subscriptions: %v", err)
	}
	if _, err := s.sysSubscribe(JetStreamDeleteConsumer, s.jsConsumerDeleteRequest); err != nil {
		return fmt.Errorf("Error setting up internal jetstream subscriptions: %v", err)
	}

	s.Noticef("----------- JETSTREAM (Beta) -----------")
	s.Noticef("  Max Memory:      %s", FriendlyBytes(cfg.MaxMemory))
	s.Noticef("  Max Storage:     %s", FriendlyBytes(cfg.MaxStore))
	s.Noticef("  Store Directory: %q", cfg.StoreDir)

	// Setup our internal system exports.
	sacc := s.SystemAccount()
	// FIXME(dlc) - Should we lock these down?
	s.Debugf("  Exports:")
	for _, export := range allJsExports {
		s.Debugf("     %s", export)
		if err := sacc.AddServiceExport(export, nil); err != nil {
			return fmt.Errorf("Error setting up jetstream service exports: %v", err)
		}
	}
	s.Noticef("----------------------------------------")

	// If we have no configured accounts setup then setup imports on global account.
	if s.globalAccountOnly() {
		if err := s.GlobalAccount().EnableJetStream(nil); err != nil {
			return fmt.Errorf("Error enabling jetstream on the global account")
		}
	}

	return nil
}

// JetStreamEnabled reports if jetstream is enabled.
func (s *Server) JetStreamEnabled() bool {
	s.mu.Lock()
	enabled := s.js != nil
	s.mu.Unlock()
	return enabled
}

// Shutdown jetstream for this server.
func (s *Server) shutdownJetStream() {
	s.mu.Lock()
	if s.js == nil {
		s.mu.Unlock()
		return
	}
	var _jsa [512]*jsAccount
	jsas := _jsa[:0]
	// Collect accounts.
	for _, jsa := range s.js.accounts {
		jsas = append(jsas, jsa)
	}
	s.mu.Unlock()

	for _, jsa := range jsas {
		jsa.flushState()
		s.js.disableJetStream(jsa)
	}

	s.mu.Lock()
	s.js.accounts = nil
	s.js = nil
	s.mu.Unlock()
}

// JetStreamConfig will return the current config. Useful if the system
// created a dynamic configuration. A copy is returned.
func (s *Server) JetStreamConfig() *JetStreamConfig {
	var c *JetStreamConfig
	s.mu.Lock()
	if s.js != nil {
		copy := s.js.config
		c = &(copy)
	}
	s.mu.Unlock()
	return c
}

// JetStreamNumAccounts returns the number of enabled accounts this server is tracking.
func (s *Server) JetStreamNumAccounts() int {
	js := s.getJetStream()
	if js == nil {
		return 0
	}
	js.mu.Lock()
	defer js.mu.Unlock()
	return len(js.accounts)
}

// JetStreamReservedResources returns the reserved resources if JetStream is enabled.
func (s *Server) JetStreamReservedResources() (int64, int64, error) {
	js := s.getJetStream()
	if js == nil {
		return -1, -1, fmt.Errorf("jetstream not enabled")
	}
	js.mu.RLock()
	defer js.mu.RUnlock()
	return js.memReserved, js.storeReserved, nil
}

func (s *Server) getJetStream() *jetStream {
	s.mu.Lock()
	js := s.js
	s.mu.Unlock()
	return js
}

// EnableJetStream will enable JetStream on this account with the defined limits.
// This is a helper for JetStreamEnableAccount.
func (a *Account) EnableJetStream(limits *JetStreamAccountLimits) error {
	a.mu.RLock()
	s := a.srv
	a.mu.RUnlock()
	if s == nil {
		return fmt.Errorf("jetstream account not registered")
	}
	// FIXME(dlc) - cluster mode
	js := s.getJetStream()
	if js == nil {
		return fmt.Errorf("jetstream not enabled")
	}

	// No limits means we dynamically set up limits.
	if limits == nil {
		limits = js.dynamicAccountLimits()
	}

	js.mu.Lock()
	// Check the limits against existing reservations.
	if err := js.sufficientResources(limits); err != nil {
		js.mu.Unlock()
		return err
	}
	if _, ok := js.accounts[a]; ok {
		js.mu.Unlock()
		return fmt.Errorf("jetstream already enabled for account")
	}
	jsa := &jsAccount{js: js, account: a, limits: *limits, streams: make(map[string]*Stream)}
	jsa.storeDir = path.Join(js.config.StoreDir, a.Name)
	js.accounts[a] = jsa
	js.reserveResources(limits)
	js.mu.Unlock()

	// Stamp inside account as well.
	a.mu.Lock()
	a.js = jsa
	a.mu.Unlock()

	// Create the proper imports here.
	sys := s.SystemAccount()
	for _, export := range allJsExports {
		if err := a.AddServiceImport(sys, export, _EMPTY_); err != nil {
			return fmt.Errorf("Error setting up jetstream service imports for account: %v", err)
		}
	}

	s.Debugf("Enabled JetStream for account %q", a.Name)
	s.Debugf("  Max Memory:      %s", FriendlyBytes(limits.MaxMemory))
	s.Debugf("  Max Storage:     %s", FriendlyBytes(limits.MaxStore))

	// Do quick fixup here for new directory structure.
	// TODO(dlc) - We can remove once we do MVP IMO.
	sdir := path.Join(jsa.storeDir, streamsDir)
	if _, err := os.Stat(sdir); os.IsNotExist(err) {
		// If we are here that means this is old school directory, upgrade in place.
		s.Noticef("  Upgrading storage directory structure for %q", a.Name)
		omdirs, _ := ioutil.ReadDir(jsa.storeDir)
		if err := os.MkdirAll(sdir, 0755); err != nil {
			return fmt.Errorf("could not create storage streams directory - %v", err)
		}
		for _, fi := range omdirs {
			os.Rename(path.Join(jsa.storeDir, fi.Name()), path.Join(sdir, fi.Name()))
		}
	}

	// Restore any state here.
	s.Noticef("  Recovering JetStream state for account %q", a.Name)

	// Check templates first since messsage sets will need proper ownership.
	tdir := path.Join(jsa.storeDir, tmplsDir)
	if stat, err := os.Stat(tdir); err == nil && stat.IsDir() {
		key := sha256.Sum256([]byte(tdir))
		hh, err := highwayhash.New64(key[:])
		if err != nil {
			return err
		}
		fis, _ := ioutil.ReadDir(tdir)
		for _, fi := range fis {
			metafile := path.Join(tdir, fi.Name(), JetStreamMetaFile)
			metasum := path.Join(tdir, fi.Name(), JetStreamMetaFileSum)
			buf, err := ioutil.ReadFile(metafile)
			if err != nil {
				s.Warnf("  Error reading StreamTemplate metafile %q: %v", metasum, err)
				continue
			}
			if _, err := os.Stat(metasum); os.IsNotExist(err) {
				s.Warnf("  Missing StreamTemplate checksum for %q", metasum)
				continue
			}
			sum, err := ioutil.ReadFile(metasum)
			if err != nil {
				s.Warnf("  Error reading StreamTemplate checksum %q: %v", metasum, err)
				continue
			}
			hh.Reset()
			hh.Write(buf)
			checksum := hex.EncodeToString(hh.Sum(nil))
			if checksum != string(sum) {
				s.Warnf("  StreamTemplate checksums do not match %q vs %q", sum, checksum)
				continue
			}
			var cfg StreamTemplateConfig
			if err := json.Unmarshal(buf, &cfg); err != nil {
				s.Warnf("  Error unmarshalling StreamTemplate metafile: %v", err)
				continue
			}
			cfg.Config.Name = _EMPTY_
			if _, err := a.AddStreamTemplate(&cfg); err != nil {
				s.Warnf("  Error recreating StreamTemplate %q: %v", cfg.Name, err)
				continue
			}
		}
	}

	fis, _ := ioutil.ReadDir(sdir)
	for _, fi := range fis {
		mdir := path.Join(sdir, fi.Name())
		key := sha256.Sum256([]byte(path.Join(mdir, msgDir)))
		hh, err := highwayhash.New64(key[:])
		if err != nil {
			return err
		}
		metafile := path.Join(mdir, JetStreamMetaFile)
		metasum := path.Join(mdir, JetStreamMetaFileSum)
		if _, err := os.Stat(metafile); os.IsNotExist(err) {
			s.Warnf("  Missing Stream metafile for %q", metafile)
			continue
		}
		buf, err := ioutil.ReadFile(metafile)
		if err != nil {
			s.Warnf("  Error reading metafile %q: %v", metasum, err)
			continue
		}
		if _, err := os.Stat(metasum); os.IsNotExist(err) {
			s.Warnf("  Missing Stream checksum for %q", metasum)
			continue
		}
		sum, err := ioutil.ReadFile(metasum)
		if err != nil {
			s.Warnf("  Error reading Stream metafile checksum %q: %v", metasum, err)
			continue
		}
		hh.Write(buf)
		checksum := hex.EncodeToString(hh.Sum(nil))
		if checksum != string(sum) {
			s.Warnf("  Stream metafile checksums do not match %q vs %q", sum, checksum)
			continue
		}

		var cfg StreamConfig
		if err := json.Unmarshal(buf, &cfg); err != nil {
			s.Warnf("  Error unmarshalling Stream metafile: %v", err)
			continue
		}
		if cfg.Template != _EMPTY_ {
			if err := jsa.addStreamNameToTemplate(cfg.Template, cfg.Name); err != nil {
				s.Warnf("  Error adding Stream %q to Template %q: %v", cfg.Name, cfg.Template, err)
			}
		}
		mset, err := a.AddStream(&cfg)
		if err != nil {
			s.Warnf("  Error recreating Stream %q: %v", cfg.Name, err)
			continue
		}

		stats := mset.State()
		s.Noticef("  Restored %s messages for Stream %q", comma(int64(stats.Msgs)), fi.Name())

		// Now do Consumers.
		odir := path.Join(sdir, fi.Name(), consumerDir)
		ofis, _ := ioutil.ReadDir(odir)
		if len(ofis) > 0 {
			s.Noticef("  Recovering %d Consumers for Stream - %q", len(ofis), fi.Name())
		}
		for _, ofi := range ofis {
			metafile := path.Join(odir, ofi.Name(), JetStreamMetaFile)
			metasum := path.Join(odir, ofi.Name(), JetStreamMetaFileSum)
			if _, err := os.Stat(metafile); os.IsNotExist(err) {
				s.Warnf("    Missing Consumer Metafile %q", metafile)
				continue
			}
			buf, err := ioutil.ReadFile(metafile)
			if err != nil {
				s.Warnf("    Error reading consumer metafile %q: %v", metasum, err)
				continue
			}
			if _, err := os.Stat(metasum); os.IsNotExist(err) {
				s.Warnf("    Missing Consumer checksum for %q", metasum)
				continue
			}
			var cfg ConsumerConfig
			if err := json.Unmarshal(buf, &cfg); err != nil {
				s.Warnf("    Error unmarshalling Consumer metafile: %v", err)
				continue
			}
			obs, err := mset.AddConsumer(&cfg)
			if err != nil {
				s.Warnf("    Error adding Consumer: %v", err)
				continue
			}
			if err := obs.readStoredState(); err != nil {
				s.Warnf("    Error restoring Consumer state: %v", err)
			}
		}
	}

	s.Noticef("JetStream state for account %q recovered", a.Name)

	return nil
}

// NumStreams will return how many streams we have.
func (a *Account) NumStreams() int {
	a.mu.RLock()
	jsa := a.js
	a.mu.RUnlock()
	if jsa == nil {
		return 0
	}
	jsa.mu.Lock()
	n := len(jsa.streams)
	jsa.mu.Unlock()
	return n
}

// Streams will return all known streams.
func (a *Account) Streams() []*Stream {
	a.mu.RLock()
	jsa := a.js
	a.mu.RUnlock()
	if jsa == nil {
		return nil
	}
	var msets []*Stream
	jsa.mu.Lock()
	for _, mset := range jsa.streams {
		msets = append(msets, mset)
	}
	jsa.mu.Unlock()
	return msets
}

func (a *Account) LookupStream(name string) (*Stream, error) {
	a.mu.RLock()
	jsa := a.js
	a.mu.RUnlock()

	if jsa == nil {
		return nil, fmt.Errorf("jetstream not enabled")
	}
	jsa.mu.Lock()
	mset, ok := jsa.streams[name]
	jsa.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("stream not found")
	}
	return mset, nil
}

// UpdateJetStreamLimits will update the account limits for a JetStream enabled account.
func (a *Account) UpdateJetStreamLimits(limits *JetStreamAccountLimits) error {
	a.mu.RLock()
	s := a.srv
	a.mu.RUnlock()
	if s == nil {
		return fmt.Errorf("jetstream account not registered")
	}

	js := s.getJetStream()
	if js == nil {
		return fmt.Errorf("jetstream not enabled")
	}

	jsa := js.lookupAccount(a)
	if jsa == nil {
		return fmt.Errorf("jetstream not enabled for account")
	}

	if limits == nil {
		limits = js.dynamicAccountLimits()
	}

	// Calculate the delta between what we have and what we want.
	jsa.mu.Lock()
	dl := diffCheckedLimits(&jsa.limits, limits)
	jsaLimits := jsa.limits
	jsa.mu.Unlock()

	js.mu.Lock()
	// Check the limits against existing reservations.
	if err := js.sufficientResources(&dl); err != nil {
		js.mu.Unlock()
		return err
	}
	// FIXME(dlc) - If we drop and are over the max on memory or store, do we delete??
	js.releaseResources(&jsaLimits)
	js.reserveResources(limits)
	js.mu.Unlock()

	// Update
	jsa.mu.Lock()
	jsa.limits = *limits
	jsa.mu.Unlock()

	return nil
}

func diffCheckedLimits(a, b *JetStreamAccountLimits) JetStreamAccountLimits {
	return JetStreamAccountLimits{
		MaxMemory: b.MaxMemory - a.MaxMemory,
		MaxStore:  b.MaxStore - a.MaxStore,
	}
}

// JetStreamUsage reports on JetStream usage and limits for an account.
func (a *Account) JetStreamUsage() JetStreamAccountStats {
	a.mu.RLock()
	jsa := a.js
	a.mu.RUnlock()

	var stats JetStreamAccountStats
	if jsa != nil {
		jsa.mu.Lock()
		stats.Memory = uint64(jsa.memUsed)
		stats.Store = uint64(jsa.storeUsed)
		stats.Streams = len(jsa.streams)
		stats.Limits = jsa.limits
		jsa.mu.Unlock()
	}
	return stats
}

// DisableJetStream will disable JetStream for this account.
func (a *Account) DisableJetStream() error {
	a.mu.Lock()
	s := a.srv
	a.js = nil
	a.mu.Unlock()

	if s == nil {
		return fmt.Errorf("jetstream account not registered")
	}

	js := s.getJetStream()
	if js == nil {
		return fmt.Errorf("jetstream not enabled")
	}

	// Remove service imports.
	for _, export := range allJsExports {
		a.removeServiceImport(export)
	}

	return js.disableJetStream(js.lookupAccount(a))
}

// Disable JetStream for the account.
func (js *jetStream) disableJetStream(jsa *jsAccount) error {
	if jsa == nil {
		return fmt.Errorf("jetstream not enabled for account")
	}

	js.mu.Lock()
	delete(js.accounts, jsa.account)
	js.releaseResources(&jsa.limits)
	js.mu.Unlock()

	jsa.delete()

	return nil
}

// Flush JetStream state for the account.
func (jsa *jsAccount) flushState() error {
	if jsa == nil {
		return fmt.Errorf("jetstream not enabled for account")
	}

	// Collect the streams.
	var _msets [64]*Stream
	msets := _msets[:0]
	jsa.mu.Lock()
	for _, mset := range jsa.streams {
		msets = append(msets, mset)
	}
	jsa.mu.Unlock()

	for _, mset := range msets {
		mset.store.Stop()
	}
	return nil
}

// JetStreamEnabled is a helper to determine if jetstream is enabled for an account.
func (a *Account) JetStreamEnabled() bool {
	if a == nil {
		return false
	}
	a.mu.RLock()
	enabled := a.js != nil
	a.mu.RUnlock()
	return enabled
}

// Updates accounting on in use memory and storage.
func (jsa *jsAccount) updateUsage(storeType StorageType, delta int64) {
	// TODO(dlc) - atomics? snapshot limits?
	jsa.mu.Lock()
	if storeType == MemoryStorage {
		jsa.memUsed += delta
	} else {
		jsa.storeUsed += delta
	}
	jsa.mu.Unlock()
}

func (jsa *jsAccount) limitsExceeded(storeType StorageType) bool {
	var exceeded bool
	jsa.mu.Lock()
	if storeType == MemoryStorage {
		if jsa.memUsed > jsa.limits.MaxMemory {
			exceeded = true
		}
	} else {
		if jsa.storeUsed > jsa.limits.MaxStore {
			exceeded = true
		}
	}
	jsa.mu.Unlock()
	return exceeded
}

// Check if a new proposed msg set while exceed our account limits.
// Lock should be held.
func (jsa *jsAccount) checkLimits(config *StreamConfig) error {
	if jsa.limits.MaxStreams > 0 && len(jsa.streams) >= jsa.limits.MaxStreams {
		return fmt.Errorf("maximum number of streams reached")
	}
	// FIXME(dlc) - Add check here for replicas based on clustering.
	if config.Replicas != 1 {
		return fmt.Errorf("replicas setting of %d not allowed", config.Replicas)
	}
	// Check MaxConsumers
	if config.MaxConsumers > 0 && config.MaxConsumers > jsa.limits.MaxConsumers {
		return fmt.Errorf("maximum consumers exceeds account limit")
	} else {
		config.MaxConsumers = jsa.limits.MaxConsumers
	}
	// Check storage, memory or disk.
	if config.MaxBytes > 0 {
		mb := config.MaxBytes * int64(config.Replicas)
		switch config.Storage {
		case MemoryStorage:
			if jsa.memReserved+mb > jsa.limits.MaxMemory {
				return fmt.Errorf("insufficient memory resources available")
			}
		case FileStorage:
			if jsa.storeReserved+mb > jsa.limits.MaxStore {
				return fmt.Errorf("insufficient storage resources available")
			}
		}
	}
	return nil
}

// Delete the JetStream resources.
func (jsa *jsAccount) delete() {
	var streams []*Stream
	var ts []string

	jsa.mu.Lock()
	for _, ms := range jsa.streams {
		streams = append(streams, ms)
	}
	acc := jsa.account
	for _, t := range jsa.templates {
		ts = append(ts, t.Name)
	}
	jsa.templates = nil
	jsa.mu.Unlock()

	for _, ms := range streams {
		ms.stop(false)
	}
	for _, t := range ts {
		acc.DeleteStreamTemplate(t)
	}
}

// Lookup the jetstream account for a given account.
func (js *jetStream) lookupAccount(a *Account) *jsAccount {
	js.mu.RLock()
	jsa := js.accounts[a]
	js.mu.RUnlock()
	return jsa
}

// Will dynamically create limits for this account.
func (js *jetStream) dynamicAccountLimits() *JetStreamAccountLimits {
	js.mu.RLock()
	// For now used all resources. Mostly meant for $G in non-account mode.
	limits := &JetStreamAccountLimits{js.config.MaxMemory, js.config.MaxStore, -1, -1}
	js.mu.RUnlock()
	return limits
}

// Check to see if we have enough system resources for this account.
// Lock should be held.
func (js *jetStream) sufficientResources(limits *JetStreamAccountLimits) error {
	if limits == nil {
		return nil
	}
	if js.memReserved+limits.MaxMemory > js.config.MaxMemory {
		return fmt.Errorf("insufficient memory resources available")
	}
	if js.storeReserved+limits.MaxStore > js.config.MaxStore {
		return fmt.Errorf("insufficient storage resources available")
	}
	return nil
}

// This will (blindly) reserve the respources requested.
// Lock should be held.
func (js *jetStream) reserveResources(limits *JetStreamAccountLimits) error {
	if limits == nil {
		return nil
	}
	if limits.MaxMemory > 0 {
		js.memReserved += limits.MaxMemory
	}
	if limits.MaxStore > 0 {
		js.storeReserved += limits.MaxStore
	}
	return nil
}

func (js *jetStream) releaseResources(limits *JetStreamAccountLimits) error {
	if limits == nil {
		return nil
	}
	if limits.MaxMemory > 0 {
		js.memReserved -= limits.MaxMemory
	}
	if limits.MaxStore > 0 {
		js.storeReserved -= limits.MaxStore
	}
	return nil
}

// Request to check if jetstream is enabled.
func (s *Server) isJsEnabledRequest(sub *subscription, c *client, subject, reply string, msg []byte) {
	if c == nil || c.acc == nil {
		return
	}
	if c.acc.JetStreamEnabled() {
		s.sendInternalAccountMsg(c.acc, reply, OK)
	} else {
		s.sendInternalAccountMsg(c.acc, reply, JetStreamNotEnabled)
	}
}

// Request for current usage and limits for this account.
func (s *Server) jsAccountInfoRequest(sub *subscription, c *client, subject, reply string, msg []byte) {
	if c == nil || c.acc == nil {
		return
	}
	if !c.acc.JetStreamEnabled() {
		s.sendInternalAccountMsg(c.acc, reply, JetStreamNotEnabled)
		return
	}
	stats := c.acc.JetStreamUsage()
	b, err := json.MarshalIndent(stats, "", "  ")
	if err != nil {
		return
	}
	s.sendInternalAccountMsg(c.acc, reply, b)
}

// Request to create a new template.
func (s *Server) jsCreateTemplateRequest(sub *subscription, c *client, subject, reply string, msg []byte) {
	if c == nil || c.acc == nil {
		return
	}
	if !c.acc.JetStreamEnabled() {
		s.sendInternalAccountMsg(c.acc, reply, JetStreamNotEnabled)
		return
	}
	var cfg StreamTemplateConfig
	if err := json.Unmarshal(msg, &cfg); err != nil {
		s.sendInternalAccountMsg(c.acc, reply, JetStreamBadRequest)
		return
	}
	templateName := subjectToken(subject, 2)
	if templateName != cfg.Name {
		s.sendInternalAccountMsg(c.acc, reply, protoErr("template name in subject does not match request"))
		return
	}

	var response = OK
	if _, err := c.acc.AddStreamTemplate(&cfg); err != nil {
		response = protoErr(err)
	}
	s.sendInternalAccountMsg(c.acc, reply, response)
}

// Request for the list of all templates.
func (s *Server) jsTemplateListRequest(sub *subscription, c *client, subject, reply string, msg []byte) {
	if c == nil || c.acc == nil {
		return
	}
	if !c.acc.JetStreamEnabled() {
		s.sendInternalAccountMsg(c.acc, reply, JetStreamNotEnabled)
		return
	}
	var names []string
	ts := c.acc.Templates()
	for _, t := range ts {
		t.mu.Lock()
		name := t.Name
		t.mu.Unlock()
		names = append(names, name)
	}
	b, err := json.MarshalIndent(names, "", "  ")
	if err != nil {
		return
	}
	s.sendInternalAccountMsg(c.acc, reply, b)
}

// Request for information about a stream template.
func (s *Server) jsTemplateInfoRequest(sub *subscription, c *client, subject, reply string, msg []byte) {
	if c == nil || c.acc == nil {
		return
	}
	if !c.acc.JetStreamEnabled() {
		s.sendInternalAccountMsg(c.acc, reply, JetStreamNotEnabled)
		return
	}
	if len(msg) != 0 {
		s.sendInternalAccountMsg(c.acc, reply, JetStreamBadRequest)
		return
	}
	name := subjectToken(subject, 2)
	t, err := c.acc.LookupStreamTemplate(name)
	if err != nil {
		s.sendInternalAccountMsg(c.acc, reply, protoErr(err))
		return
	}
	t.mu.Lock()
	cfg := t.StreamTemplateConfig.deepCopy()
	streams := t.streams
	t.mu.Unlock()
	si := &StreamTemplateInfo{
		Config:  cfg,
		Streams: streams,
	}
	b, err := json.MarshalIndent(si, "", "  ")
	if err != nil {
		return
	}
	s.sendInternalAccountMsg(c.acc, reply, b)
}

// Request to delete a stream template.
func (s *Server) jsTemplateDeleteRequest(sub *subscription, c *client, subject, reply string, msg []byte) {
	if c == nil || c.acc == nil {
		return
	}
	if !c.acc.JetStreamEnabled() {
		s.sendInternalAccountMsg(c.acc, reply, JetStreamNotEnabled)
		return
	}
	if len(msg) != 0 {
		s.sendInternalAccountMsg(c.acc, reply, JetStreamBadRequest)
		return
	}
	name := subjectToken(subject, 2)
	err := c.acc.DeleteStreamTemplate(name)
	if err != nil {
		s.sendInternalAccountMsg(c.acc, reply, protoErr(err))
		return
	}
	s.sendInternalAccountMsg(c.acc, reply, OK)
}

// Request to create a stream.
func (s *Server) jsCreateStreamRequest(sub *subscription, c *client, subject, reply string, msg []byte) {
	if c == nil || c.acc == nil {
		return
	}
	if !c.acc.JetStreamEnabled() {
		s.sendInternalAccountMsg(c.acc, reply, JetStreamNotEnabled)
		return
	}
	var cfg StreamConfig
	if err := json.Unmarshal(msg, &cfg); err != nil {
		s.sendInternalAccountMsg(c.acc, reply, JetStreamBadRequest)
		return
	}
	streamName := subjectToken(subject, 2)
	if streamName != cfg.Name {
		s.sendInternalAccountMsg(c.acc, reply, protoErr("stream name in subject does not match request"))
		return
	}

	var response = OK
	if _, err := c.acc.AddStream(&cfg); err != nil {
		response = protoErr(err)
	}
	s.sendInternalAccountMsg(c.acc, reply, response)
}

// Request for the list of all streams.
func (s *Server) jsStreamListRequest(sub *subscription, c *client, subject, reply string, msg []byte) {
	if c == nil || c.acc == nil {
		return
	}
	if !c.acc.JetStreamEnabled() {
		s.sendInternalAccountMsg(c.acc, reply, JetStreamNotEnabled)
		return
	}
	var names []string
	msets := c.acc.Streams()
	for _, mset := range msets {
		names = append(names, mset.Name())
	}
	b, err := json.MarshalIndent(names, "", "  ")
	if err != nil {
		return
	}
	s.sendInternalAccountMsg(c.acc, reply, b)
}

// Request for information about a stream.
func (s *Server) jsStreamInfoRequest(sub *subscription, c *client, subject, reply string, msg []byte) {
	if c == nil || c.acc == nil {
		return
	}
	if !c.acc.JetStreamEnabled() {
		s.sendInternalAccountMsg(c.acc, reply, JetStreamNotEnabled)
		return
	}
	if len(msg) != 0 {
		s.sendInternalAccountMsg(c.acc, reply, JetStreamBadRequest)
		return
	}
	name := subjectToken(subject, 2)
	mset, err := c.acc.LookupStream(name)
	if err != nil {
		s.sendInternalAccountMsg(c.acc, reply, protoErr(err))
		return
	}
	msi := StreamInfo{
		State:  mset.State(),
		Config: mset.Config(),
	}
	b, err := json.MarshalIndent(msi, "", "  ")
	if err != nil {
		return
	}
	s.sendInternalAccountMsg(c.acc, reply, b)
}

// Request to delete a stream.
func (s *Server) jsStreamDeleteRequest(sub *subscription, c *client, subject, reply string, msg []byte) {
	if c == nil || c.acc == nil {
		return
	}
	if !c.acc.JetStreamEnabled() {
		s.sendInternalAccountMsg(c.acc, reply, JetStreamNotEnabled)
		return
	}
	if len(msg) != 0 {
		s.sendInternalAccountMsg(c.acc, reply, JetStreamBadRequest)
		return
	}
	name := subjectToken(subject, 2)
	mset, err := c.acc.LookupStream(name)
	if err != nil {
		s.sendInternalAccountMsg(c.acc, reply, protoErr(err))
		return
	}
	var response = OK
	if err := mset.Delete(); err != nil {
		response = protoErr(err)
	}
	s.sendInternalAccountMsg(c.acc, reply, response)
}

// Request to delete a message.
// This expects a stream sequence number as the msg body.
func (s *Server) jsMsgDeleteRequest(sub *subscription, c *client, subject, reply string, msg []byte) {
	if c == nil || c.acc == nil {
		return
	}
	if !c.acc.JetStreamEnabled() {
		s.sendInternalAccountMsg(c.acc, reply, JetStreamNotEnabled)
		return
	}
	if len(msg) == 0 {
		s.sendInternalAccountMsg(c.acc, reply, JetStreamBadRequest)
		return
	}
	name := subjectToken(subject, 2)
	mset, err := c.acc.LookupStream(name)
	if err != nil {
		s.sendInternalAccountMsg(c.acc, reply, protoErr(err))
		return
	}
	var response = OK
	seq, _ := strconv.Atoi(string(msg))
	if !mset.EraseMsg(uint64(seq)) {
		response = protoErr(fmt.Sprintf("sequence [%d] not found", seq))
	}
	s.sendInternalAccountMsg(c.acc, reply, response)
}

// Request to purge a stream.
func (s *Server) jsStreamPurgeRequest(sub *subscription, c *client, subject, reply string, msg []byte) {
	if c == nil || c.acc == nil {
		return
	}
	if !c.acc.JetStreamEnabled() {
		s.sendInternalAccountMsg(c.acc, reply, JetStreamNotEnabled)
		return
	}
	if len(msg) != 0 {
		s.sendInternalAccountMsg(c.acc, reply, JetStreamBadRequest)
		return
	}
	name := subjectToken(subject, 2)
	mset, err := c.acc.LookupStream(name)
	if err != nil {
		s.sendInternalAccountMsg(c.acc, reply, protoErr(err))
		return
	}

	mset.Purge()
	s.sendInternalAccountMsg(c.acc, reply, OK)
}

// Request to create a durable consumer.
func (s *Server) jsCreateConsumerRequest(sub *subscription, c *client, subject, reply string, msg []byte) {
	if c == nil || c.acc == nil {
		return
	}
	if !c.acc.JetStreamEnabled() {
		s.sendInternalAccountMsg(c.acc, reply, JetStreamNotEnabled)
		return
	}
	var req CreateConsumerRequest
	if err := json.Unmarshal(msg, &req); err != nil {
		s.sendInternalAccountMsg(c.acc, reply, JetStreamBadRequest)
		return
	}
	streamName := subjectToken(subject, 2)
	if streamName != req.Stream {
		s.sendInternalAccountMsg(c.acc, reply, protoErr("stream name in subject does not match request"))
		return
	}
	stream, err := c.acc.LookupStream(req.Stream)
	if err != nil {
		s.sendInternalAccountMsg(c.acc, reply, protoErr(err))
		return
	}
	// Now check we do not have a durable.
	if req.Config.Durable == _EMPTY_ {
		s.sendInternalAccountMsg(c.acc, reply, protoErr("consumer expected to be durable but a durable name was not set"))
		return
	}
	consumerName := subjectToken(subject, 4)
	if consumerName != req.Config.Durable {
		s.sendInternalAccountMsg(c.acc, reply, protoErr("consumer name in subject does not match durable name in request"))
		return
	}
	var response = OK
	if _, err := stream.AddConsumer(&req.Config); err != nil {
		response = protoErr(err)
	}
	s.sendInternalAccountMsg(c.acc, reply, response)
}

// Request to create an ephemeral consumer.
func (s *Server) jsCreateEphemeralConsumerRequest(sub *subscription, c *client, subject, reply string, msg []byte) {
	if c == nil || c.acc == nil {
		return
	}
	if !c.acc.JetStreamEnabled() {
		s.sendInternalAccountMsg(c.acc, reply, JetStreamNotEnabled)
		return
	}
	var req CreateConsumerRequest
	if err := json.Unmarshal(msg, &req); err != nil {
		s.sendInternalAccountMsg(c.acc, reply, JetStreamBadRequest)
		return
	}
	streamName := subjectToken(subject, 2)
	if streamName != req.Stream {
		s.sendInternalAccountMsg(c.acc, reply, protoErr("stream name in subject does not match request"))
		return
	}
	stream, err := c.acc.LookupStream(req.Stream)
	if err != nil {
		s.sendInternalAccountMsg(c.acc, reply, protoErr(err))
		return
	}
	// Now check we do not have a durable.
	if req.Config.Durable != _EMPTY_ {
		s.sendInternalAccountMsg(c.acc, reply, protoErr("consumer expected to be ephemeral but a durable name was set"))
		return
	}
	var response = OK
	if o, err := stream.AddConsumer(&req.Config); err != nil {
		response = protoErr(err)
	} else if !o.isDurable() {
		// If the consumer is ephemeral add in the name
		response = OK + " " + o.Name()
	}
	s.sendInternalAccountMsg(c.acc, reply, response)
}

// Request for the list of all consumers.
func (s *Server) jsConsumersRequest(sub *subscription, c *client, subject, reply string, msg []byte) {
	if c == nil || c.acc == nil {
		return
	}
	if !c.acc.JetStreamEnabled() {
		s.sendInternalAccountMsg(c.acc, reply, JetStreamNotEnabled)
		return
	}
	if len(msg) != 0 {
		s.sendInternalAccountMsg(c.acc, reply, JetStreamBadRequest)
		return
	}
	name := subjectToken(subject, 2)
	mset, err := c.acc.LookupStream(name)
	if err != nil {
		s.sendInternalAccountMsg(c.acc, reply, protoErr(err))
		return
	}
	var onames []string
	obs := mset.Consumers()
	for _, o := range obs {
		onames = append(onames, o.Name())
	}
	b, err := json.MarshalIndent(onames, "", "  ")
	if err != nil {
		return
	}
	s.sendInternalAccountMsg(c.acc, reply, b)
}

// Request for information about an consumer.
func (s *Server) jsConsumerInfoRequest(sub *subscription, c *client, subject, reply string, msg []byte) {
	if c == nil || c.acc == nil {
		return
	}
	if !c.acc.JetStreamEnabled() {
		s.sendInternalAccountMsg(c.acc, reply, JetStreamNotEnabled)
		return
	}
	if len(msg) != 0 {
		s.sendInternalAccountMsg(c.acc, reply, JetStreamBadRequest)
		return
	}
	stream := subjectToken(subject, 2)
	mset, err := c.acc.LookupStream(stream)
	if err != nil {
		s.sendInternalAccountMsg(c.acc, reply, protoErr(err))
		return
	}
	consumer := subjectToken(subject, 4)
	obs := mset.LookupConsumer(consumer)
	if obs == nil {
		s.sendInternalAccountMsg(c.acc, reply, protoErr("consumer not found"))
		return
	}
	info := obs.Info()
	b, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return
	}
	s.sendInternalAccountMsg(c.acc, reply, b)
}

// Request to delete an Consumer.
func (s *Server) jsConsumerDeleteRequest(sub *subscription, c *client, subject, reply string, msg []byte) {
	if c == nil || c.acc == nil {
		return
	}
	if !c.acc.JetStreamEnabled() {
		s.sendInternalAccountMsg(c.acc, reply, JetStreamNotEnabled)
		return
	}
	if len(msg) != 0 {
		s.sendInternalAccountMsg(c.acc, reply, JetStreamBadRequest)
		return
	}
	stream := subjectToken(subject, 2)
	mset, err := c.acc.LookupStream(stream)
	if err != nil {
		s.sendInternalAccountMsg(c.acc, reply, protoErr(err))
		return
	}
	consumer := subjectToken(subject, 4)
	obs := mset.LookupConsumer(consumer)
	if obs == nil {
		s.sendInternalAccountMsg(c.acc, reply, protoErr("consumer not found"))
		return
	}
	var response = OK
	if err := obs.Delete(); err != nil {
		response = protoErr(err)
	}
	s.sendInternalAccountMsg(c.acc, reply, response)
}

const (
	// JetStreamStoreDir is the prefix we use.
	JetStreamStoreDir = "jetstream"
	// JetStreamMaxStoreDefault is the default disk storage limit. 1TB
	JetStreamMaxStoreDefault = 1024 * 1024 * 1024 * 1024
	// JetStreamMaxMemDefault is only used when we can't determine system memory. 256MB
	JetStreamMaxMemDefault = 1024 * 1024 * 256
)

// Dynamically create a config with a tmp based directory (repeatable) and 75% of system memory.
func (s *Server) dynJetStreamConfig(storeDir string) *JetStreamConfig {
	jsc := &JetStreamConfig{}
	if storeDir != "" {
		jsc.StoreDir = filepath.Join(storeDir, JetStreamStoreDir)
	} else {
		jsc.StoreDir = filepath.Join(os.TempDir(), JetStreamStoreDir)
	}
	jsc.MaxStore = JetStreamMaxStoreDefault
	// Estimate to 75% of total memory if we can determine system memory.
	if sysMem := sysmem.Memory(); sysMem > 0 {
		jsc.MaxMemory = sysMem / 4 * 3
	} else {
		jsc.MaxMemory = JetStreamMaxMemDefault
	}
	return jsc
}

// Helper function.
func (a *Account) checkForJetStream() (*Server, *jsAccount, error) {
	a.mu.RLock()
	s := a.srv
	jsa := a.js
	a.mu.RUnlock()

	if s == nil {
		return nil, nil, fmt.Errorf("jetstream account not registered")
	}

	if jsa == nil {
		return nil, nil, fmt.Errorf("jetstream not enabled for account")
	}

	return s, jsa, nil
}

// StreamTemplateConfig allows a configuration to auto-create streams based on this template when a message
// is received that matches. Each new stream will use the config as the template config to create them.
type StreamTemplateConfig struct {
	Name       string        `json:"name"`
	Config     *StreamConfig `json:"config"`
	MaxStreams uint32        `json:"max_streams"`
}

// StreamTemplateInfo
type StreamTemplateInfo struct {
	Config  *StreamTemplateConfig `json:"config"`
	Streams []string              `json:"streams"`
}

// StreamTemplate
type StreamTemplate struct {
	mu  sync.Mutex
	tc  *client
	jsa *jsAccount
	*StreamTemplateConfig
	streams []string
}

func (t *StreamTemplateConfig) deepCopy() *StreamTemplateConfig {
	copy := *t
	cfg := *t.Config
	copy.Config = &cfg
	return &copy
}

// AddStreamTemplate will add a stream template to this account that allows auto-creation of streams.
func (a *Account) AddStreamTemplate(tc *StreamTemplateConfig) (*StreamTemplate, error) {
	s, jsa, err := a.checkForJetStream()
	if err != nil {
		return nil, err
	}
	if tc.Config.Name != "" {
		return nil, fmt.Errorf("template config name should be empty")
	}

	// FIXME(dlc) - Hacky
	tcopy := tc.deepCopy()
	tcopy.Config.Name = "_"
	cfg, err := checkStreamCfg(tcopy.Config)
	if err != nil {
		return nil, err
	}
	tcopy.Config = &cfg
	t := &StreamTemplate{
		StreamTemplateConfig: tcopy,
		tc:                   s.createInternalJetStreamClient(),
		jsa:                  jsa,
	}
	t.tc.registerWithAccount(a)

	jsa.mu.Lock()
	if jsa.templates == nil {
		jsa.templates = make(map[string]*StreamTemplate)
		// Create the appropriate store
		if cfg.Storage == FileStorage {
			jsa.store = newTemplateFileStore(jsa.storeDir)
		} else {
			jsa.store = newTemplateMemStore()
		}
	} else if _, ok := jsa.templates[tcopy.Name]; ok {
		jsa.mu.Unlock()
		return nil, fmt.Errorf("template with name %q already exists", tcopy.Name)
	}
	jsa.templates[tcopy.Name] = t
	jsa.mu.Unlock()

	// FIXME(dlc) - we can not overlap subjects between templates. Need to have test.

	// Setup the internal subscriptions to trap the messages.
	if err := t.createTemplateSubscriptions(); err != nil {
		return nil, err
	}
	if err := jsa.store.Store(t); err != nil {
		t.Delete()
		return nil, err
	}
	return t, nil
}

func (t *StreamTemplate) createTemplateSubscriptions() error {
	if t == nil {
		return fmt.Errorf("no template")
	}
	if t.tc == nil {
		return fmt.Errorf("template not enabled")
	}
	c := t.tc
	if !c.srv.eventsEnabled() {
		return ErrNoSysAccount
	}
	sid := 1
	for _, subject := range t.Config.Subjects {
		// Now create the subscription
		sub, err := c.processSub([]byte(subject+" "+strconv.Itoa(sid)), false)
		if err != nil {
			c.acc.DeleteStreamTemplate(t.Name)
			return err
		}
		c.mu.Lock()
		sub.icb = t.processInboundTemplateMsg
		c.mu.Unlock()
		sid++
	}
	return nil
}

func (t *StreamTemplate) processInboundTemplateMsg(_ *subscription, _ *client, subject, reply string, msg []byte) {
	if t == nil || t.jsa == nil {
		return
	}
	jsa := t.jsa
	cn := CanonicalName(subject)

	jsa.mu.Lock()
	// If we already are registered then we can just return here.
	if _, ok := jsa.streams[cn]; ok {
		jsa.mu.Unlock()
		return
	}
	acc := jsa.account
	jsa.mu.Unlock()

	// Check if we are at the maximum and grab some variables.
	t.mu.Lock()
	c := t.tc
	cfg := *t.Config
	cfg.Template = t.Name
	atLimit := len(t.streams) >= int(t.MaxStreams)
	if !atLimit {
		t.streams = append(t.streams, cn)
	}
	t.mu.Unlock()

	if atLimit {
		c.Warnf("JetStream could not create stream for account %q on subject %q, at limit", acc.Name, subject)
		return
	}

	// We need to create the stream here.
	// Change the config from the template and only use literal subject.
	cfg.Name = cn
	cfg.Subjects = []string{subject}
	mset, err := acc.AddStream(&cfg)
	if err != nil {
		acc.validateStreams(t)
		c.Warnf("JetStream could not create stream for account %q on subject %q", acc.Name, subject)
		return
	}

	// Process this message directly by invoking mset.
	mset.processInboundJetStreamMsg(nil, nil, subject, reply, msg)
}

// LookupStreamTemplate looks up the names stream template.
func (a *Account) LookupStreamTemplate(name string) (*StreamTemplate, error) {
	_, jsa, err := a.checkForJetStream()
	if err != nil {
		return nil, err
	}
	jsa.mu.Lock()
	defer jsa.mu.Unlock()
	if jsa.templates == nil {
		return nil, fmt.Errorf("no template found")
	}
	t, ok := jsa.templates[name]
	if !ok {
		return nil, fmt.Errorf("no template found")
	}
	return t, nil
}

// This function will check all named streams and make sure they are valid.
func (a *Account) validateStreams(t *StreamTemplate) {
	t.mu.Lock()
	var vstreams []string
	for _, sname := range t.streams {
		if _, err := a.LookupStream(sname); err == nil {
			vstreams = append(vstreams, sname)
		}
	}
	t.streams = vstreams
	t.mu.Unlock()
}

func (t *StreamTemplate) Delete() error {
	if t == nil {
		return fmt.Errorf("nil stream template")
	}

	t.mu.Lock()
	jsa := t.jsa
	c := t.tc
	t.tc = nil
	defer func() {
		if c != nil {
			c.closeConnection(ClientClosed)
		}
	}()
	t.mu.Unlock()

	if jsa == nil {
		return fmt.Errorf("jetstream not enabled")
	}

	jsa.mu.Lock()
	if jsa.templates == nil {
		jsa.mu.Unlock()
		return fmt.Errorf("no template found")
	}
	if _, ok := jsa.templates[t.Name]; !ok {
		jsa.mu.Unlock()
		return fmt.Errorf("no template found")
	}
	delete(jsa.templates, t.Name)
	acc := jsa.account
	jsa.mu.Unlock()

	// Remove streams associated with this template.
	var streams []*Stream
	t.mu.Lock()
	for _, name := range t.streams {
		if mset, err := acc.LookupStream(name); err == nil {
			streams = append(streams, mset)
		}
	}
	t.mu.Unlock()

	if jsa.store != nil {
		if err := jsa.store.Delete(t); err != nil {
			return fmt.Errorf("error deleting template from store: %v", err)
		}
	}

	var lastErr error
	for _, mset := range streams {
		if err := mset.Delete(); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

func (a *Account) DeleteStreamTemplate(name string) error {
	t, err := a.LookupStreamTemplate(name)
	if err != nil {
		return err
	}
	return t.Delete()
}

func (a *Account) Templates() []*StreamTemplate {
	var ts []*StreamTemplate
	_, jsa, err := a.checkForJetStream()
	if err != nil {
		return nil
	}

	jsa.mu.Lock()
	for _, t := range jsa.templates {
		// FIXME(dlc) - Copy?
		ts = append(ts, t)
	}
	jsa.mu.Unlock()

	return ts
}

// Will add a stream to a template, this is for recovery.
func (jsa *jsAccount) addStreamNameToTemplate(tname, mname string) error {
	if jsa.templates == nil {
		return fmt.Errorf("no template found")
	}
	t, ok := jsa.templates[tname]
	if !ok {
		return fmt.Errorf("no template found")
	}
	// We found template.
	t.mu.Lock()
	t.streams = append(t.streams, mname)
	t.mu.Unlock()
	return nil
}

// This will check if a template owns this stream.
// jsAccount lock should be held
func (jsa *jsAccount) checkTemplateOwnership(tname, sname string) bool {
	if jsa.templates == nil {
		return false
	}
	t, ok := jsa.templates[tname]
	if !ok {
		return false
	}
	// We found template, make sure we are in streams.
	for _, streamName := range t.streams {
		if sname == streamName {
			return true
		}
	}
	return false
}

// FriendlyBytes returns a string with the given bytes int64
// represented as a size, such as 1KB, 10MB, etc...
func FriendlyBytes(bytes int64) string {
	fbytes := float64(bytes)
	base := 1024
	pre := []string{"K", "M", "G", "T", "P", "E"}
	if fbytes < float64(base) {
		return fmt.Sprintf("%v B", fbytes)
	}
	exp := int(math.Log(fbytes) / math.Log(float64(base)))
	index := exp - 1
	return fmt.Sprintf("%.2f %sB", fbytes/math.Pow(float64(base), float64(exp)), pre[index])
}

func isValidName(name string) bool {
	if name == "" {
		return false
	}
	return !strings.ContainsAny(name, ".*>")
}

// CanonicalName will replace all token separators '.' with '_'.
// This can be used when naming streams or consumers with multi-token subjects.
func CanonicalName(name string) string {
	return strings.ReplaceAll(name, ".", "_")
}

func protoErr(err interface{}) string {
	return fmt.Sprintf("%s '%v'", ErrPrefix, err)
}
