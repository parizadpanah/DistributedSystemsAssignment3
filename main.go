package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"kvstore/pb"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type record struct {
	Collection string          `json:"collection"`
	Key        string          `json:"key"`
	Value      json.RawMessage `json:"value"`
	TS         time.Time       `json:"ts"`
	Tombstone  bool            `json:"tombstone"`
}

type entry struct {
	Value json.RawMessage
}

type collectionState struct {
	mu        sync.RWMutex
	file      *os.File
	writer    *bufio.Writer
	index     map[string]entry
	lines     int
	lastFlush time.Time
}

type transaction struct {
	ID         string
	Collection string
	Key        string
	Value      json.RawMessage
	State      string // "preparing", "prepared", "committed", "aborted"
	CreatedAt  time.Time
}

type store struct {
	dir  string
	mu   sync.RWMutex
	cols map[string]*collectionState
	
	// 2PC transaction management
	txnMu        sync.RWMutex
	transactions map[string]*transaction
}

func openStore(dir string) (*store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &store{
		dir:          dir,
		cols:         make(map[string]*collectionState),
		transactions: make(map[string]*transaction),
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			coll := strings.TrimSuffix(e.Name(), ".jsonl")
			s.openCollection(coll)
		}
	}
	s.openCollection("default")
	
	
	go s.cleanupOldTransactions()
	
	return s, nil
}

func (s *store) cleanupOldTransactions() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	
	for range ticker.C {
		s.txnMu.Lock()
		now := time.Now()
		for id, txn := range s.transactions {
			if now.Sub(txn.CreatedAt) > time.Minute {
				log.Printf("[Cleanup] Removing old transaction: %s (state: %s)", id, txn.State)
				delete(s.transactions, id)
			}
		}
		s.txnMu.Unlock()
	}
}

// -----------------------2PC Transaction Management-----------------------

func (s *store) prepareTxn(txnID, collection, key string, value json.RawMessage) error {
	s.txnMu.Lock()
	defer s.txnMu.Unlock()
	
	// Check if transaction already exists
	if txn, exists := s.transactions[txnID]; exists {
		if txn.State == "prepared" {
			return nil // Already prepared
		}
		return fmt.Errorf("transaction %s already exists in state %s", txnID, txn.State)
	}
	
	txn := &transaction{
		ID:         txnID,
		Collection: collection,
		Key:        key,
		Value:      value,
		State:      "prepared",
		CreatedAt:  time.Now(),
	}
	
	s.transactions[txnID] = txn
	log.Printf("[2PC] Transaction %s prepared: col=%s key=%s", txnID, collection, key)
	return nil
}

func (s *store) commitTxn(txnID string) error {
	s.txnMu.Lock()
	txn, exists := s.transactions[txnID]
	if !exists {
		s.txnMu.Unlock()
		return fmt.Errorf("transaction %s not found", txnID)
	}
	
	if txn.State != "prepared" {
		s.txnMu.Unlock()
		return fmt.Errorf("transaction %s not in prepared state (current: %s)", txnID, txn.State)
	}
	
	txn.State = "committed"
	s.txnMu.Unlock()
	
	err := s.put(txn.Collection, txn.Key, txn.Value)
	if err != nil {
		log.Printf("[2PC] Commit failed for transaction %s: %v", txnID, err)
		return err
	}
	
	s.txnMu.Lock()
	delete(s.transactions, txnID)
	s.txnMu.Unlock()
	
	log.Printf("[2PC] Transaction %s committed successfully", txnID)
	return nil
}

func (s *store) abortTxn(txnID string) error {
	s.txnMu.Lock()
	defer s.txnMu.Unlock()
	
	txn, exists := s.transactions[txnID]
	if !exists {
		return nil 
	}
	
	txn.State = "aborted"
	delete(s.transactions, txnID)
	
	log.Printf("[2PC] Transaction %s aborted", txnID)
	return nil
}

// -----------------------Collection implementation-----------------------

func (s *store) openCollection(name string) (*collectionState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.cols[name]; ok {
		return st, nil
	}

	fp := filepath.Join(s.dir, name+".jsonl")
	f, err := os.OpenFile(fp, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}

	st := &collectionState{
		file:   f,
		writer: bufio.NewWriterSize(f, 256*1024),
		index:  make(map[string]entry),
	}

	f.Seek(0, io.SeekStart)
	sc := bufio.NewScanner(f)
	buf := make([]byte, 0, 1024*1024)
	sc.Buffer(buf, 10*1024*1024)

	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var r record
		if err := json.Unmarshal(line, &r); err == nil {
			if !r.Tombstone {
				st.index[r.Key] = entry{Value: r.Value}
			} else {
				delete(st.index, r.Key)
			}
			st.lines++
		}
	}
	f.Seek(0, io.SeekEnd)
	s.cols[name] = st
	return st, nil
}

// -----------------------PUT/objects-----------------------

func (s *store) put(collection, key string, value json.RawMessage) error {
	if collection == "" {
		collection = "default"
	}
	st, err := s.openCollection(collection)
	if err != nil {
		return err
	}

	st.mu.Lock()
	defer st.mu.Unlock()

	// Append JSON-encoded record to the collection file buffer
	rec := record{
		Collection: collection,
		Key:        key,
		Value:      value,
		TS:         time.Now().UTC(),
	}
	b, _ := json.Marshal(rec)

	if _, err := st.writer.Write(b); err != nil {
		return err
	}
	st.writer.WriteString("\n")
	st.writer.Flush()
	st.index[key] = entry{Value: value}
	st.lines++
	return nil
}

// -----------------------GET/objects{key}-----------------------

func (s *store) get(collection, key string) (json.RawMessage, bool) {
	if collection == "" {
		collection = "default"
	}
	st, err := s.openCollection(collection)
	if err != nil {
		return nil, false
	}
	st.mu.RLock()
	defer st.mu.RUnlock()
	e, ok := st.index[key]
	return e.Value, ok
}

// -----------------------GET/objects-----------------------

func (s *store) list(collection, prefix string, w io.Writer) {
	s.mu.RLock()
	var targets []string
	if collection != "" {
		targets = []string{collection}
	} else {
		for k := range s.cols {
			targets = append(targets, k)
		}
	}
	s.mu.RUnlock()

	w.Write([]byte("["))
	first := true
	for _, c := range targets {
		st, _ := s.openCollection(c)
		if st == nil {
			continue
		}
		st.mu.RLock()
		for k, v := range st.index {
			if prefix == "" || strings.HasPrefix(k, prefix) {
				if !first {
					w.Write([]byte(","))
				}
				first = false
				item := struct {
					Coll  string          `json:"collection"`
					Key   string          `json:"key"`
					Value json.RawMessage `json:"value"`
				}{c, k, v.Value}
				json.NewEncoder(w).Encode(item)
			}
		}
		st.mu.RUnlock()
	}
	w.Write([]byte("]"))
}

// -----------------------gRPC Server-----------------------

type grpcServer struct {
	pb.UnimplementedReplicationServer
	st *store
}

func (s *grpcServer) Replicate(ctx context.Context, req *pb.ReplicateRequest) (*pb.ReplicateResponse, error) {
	log.Printf("[Backup] Received replication: col=%s key=%s", req.Collection, req.Key)
	err := s.st.put(req.Collection, req.Key, json.RawMessage(req.Value))
	if err != nil {
		return &pb.ReplicateResponse{Success: false, Error: err.Error()}, nil
	}
	return &pb.ReplicateResponse{Success: true}, nil
}

func (s *grpcServer) Prepare(ctx context.Context, req *pb.PrepareRequest) (*pb.PrepareResponse, error) {
	log.Printf("[2PC-Prepare] txn=%s col=%s key=%s", req.TransactionId, req.Collection, req.Key)
	
	err := s.st.prepareTxn(req.TransactionId, req.Collection, req.Key, json.RawMessage(req.Value))
	if err != nil {
		log.Printf("[2PC-Prepare] VOTE NO for txn %s: %v", req.TransactionId, err)
		return &pb.PrepareResponse{Vote: false, Error: err.Error()}, nil
	}
	
	log.Printf("[2PC-Prepare] VOTE YES for txn %s", req.TransactionId)
	return &pb.PrepareResponse{Vote: true}, nil
}

func (s *grpcServer) Commit(ctx context.Context, req *pb.CommitRequest) (*pb.CommitResponse, error) {
	log.Printf("[2PC-Commit] txn=%s", req.TransactionId)
	
	err := s.st.commitTxn(req.TransactionId)
	if err != nil {
		log.Printf("[2PC-Commit] FAILED for txn %s: %v", req.TransactionId, err)
		return &pb.CommitResponse{Success: false, Error: err.Error()}, nil
	}
	
	log.Printf("[2PC-Commit] SUCCESS for txn %s", req.TransactionId)
	return &pb.CommitResponse{Success: true}, nil
}

func (s *grpcServer) Abort(ctx context.Context, req *pb.AbortRequest) (*pb.AbortResponse, error) {
	log.Printf("[2PC-Abort] txn=%s", req.TransactionId)
	
	err := s.st.abortTxn(req.TransactionId)
	if err != nil {
		return &pb.AbortResponse{Success: false, Error: err.Error()}, nil
	}
	
	return &pb.AbortResponse{Success: true}, nil
}

// -----------------------HTTP Server-----------------------

type apiServer struct {
	st          *store
	role        string
	backupPeers []pb.ReplicationClient
}

func (s *apiServer) handlePut(w http.ResponseWriter, r *http.Request) {
	if s.role != "primary" {
		http.Error(w, "I am a backup, I cannot accept writes directly", http.StatusForbidden)
		return
	}

	var req struct {
		Key   string          `json:"key"`
		Value json.RawMessage `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	col := r.URL.Query().Get("collection")

	// Generate transaction ID
	txnID := fmt.Sprintf("txn-%d", time.Now().UnixNano())
	
	// PHASE 1: PREPARE
	log.Printf("[2PC] Starting transaction %s", txnID)
	
	prepareCtx, prepareCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer prepareCancel()
	
	var wg sync.WaitGroup
	votes := make([]bool, len(s.backupPeers))
	errors := make([]error, len(s.backupPeers))
	
	for i, client := range s.backupPeers {
		wg.Add(1)
		go func(idx int, c pb.ReplicationClient) {
			defer wg.Done()
			
			prepReq := &pb.PrepareRequest{
				TransactionId: txnID,
				Collection:    col,
				Key:           req.Key,
				Value:         []byte(req.Value),
			}
			
			resp, err := c.Prepare(prepareCtx, prepReq)
			if err != nil {
				log.Printf("[2PC] Prepare RPC failed for peer %d: %v", idx, err)
				errors[idx] = err
				votes[idx] = false
			} else {
				votes[idx] = resp.Vote
				if !resp.Vote {
					errors[idx] = fmt.Errorf("peer voted NO: %s", resp.Error)
				}
			}
		}(i, client)
	}
	
	wg.Wait()
	
	// Check if all peers voted YES
	allYes := true
	for i, vote := range votes {
		if !vote {
			allYes = false
			log.Printf("[2PC] Peer %d voted NO or failed: %v", i, errors[i])
		}
	}
	
	// PHASE 2: COMMIT or ABORT
	if allYes {
		log.Printf("[2PC] All peers voted YES, proceeding to COMMIT")
		
		if err := s.st.put(col, req.Key, req.Value); err != nil {
			log.Printf("[2PC] Primary write failed: %v", err)
			s.sendAbortToAll(txnID)
			http.Error(w, "storage error", http.StatusInternalServerError)
			return
		}
		
		commitCtx, commitCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer commitCancel()
		
		commitSuccess := 0
		for i, client := range s.backupPeers {
			wg.Add(1)
			go func(idx int, c pb.ReplicationClient) {
				defer wg.Done()
				
				commitReq := &pb.CommitRequest{TransactionId: txnID}
				resp, err := c.Commit(commitCtx, commitReq)
				if err != nil || !resp.Success {
					log.Printf("[2PC] Commit failed for peer %d: %v", idx, err)
				} else {
					commitSuccess++
				}
			}(i, client)
		}
		wg.Wait()
		
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fmt.Sprintf(`{"status":"ok", "committed_to": %d}`, commitSuccess)))
		log.Printf("[2PC] Transaction %s COMMITTED to %d peers", txnID, commitSuccess)
		
	} else {
		log.Printf("[2PC] Some peers voted NO, sending ABORT to all")
		s.sendAbortToAll(txnID)
		http.Error(w, "transaction aborted: not all replicas agreed", http.StatusConflict)
	}
}

func (s *apiServer) sendAbortToAll(txnID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	
	var wg sync.WaitGroup
	for _, client := range s.backupPeers {
		wg.Add(1)
		go func(c pb.ReplicationClient) {
			defer wg.Done()
			abortReq := &pb.AbortRequest{TransactionId: txnID}
			c.Abort(ctx, abortReq)
		}(client)
	}
	wg.Wait()
}

func (s *apiServer) handleGet(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/objects/")
	if key == "" {
		s.st.list(r.URL.Query().Get("collection"), r.URL.Query().Get("prefix"), w)
		return
	}
	if k, err := url.PathUnescape(key); err == nil {
		key = k
	}
	val, ok := s.st.get(r.URL.Query().Get("collection"), key)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(val)
}

// -----------------------MAIN-----------------------

func main() {
	role := flag.String("role", "primary", "Node role: primary or backup")
	httpPort := flag.Int("http-port", 8080, "HTTP server port")
	grpcPort := flag.Int("grpc-port", 50051, "gRPC server port (internal)")
	peerAddrs := flag.String("peers", "", "Comma-separated list of backup addresses (e.g. backup1:50051,backup2:50051)")
	dataDir := flag.String("data", "./data", "Data directory")
	flag.Parse()

	st, err := openStore(*dataDir)
	if err != nil {
		log.Fatalf("Failed to open store: %v", err)
	}

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", *grpcPort))
	if err != nil {
		log.Fatalf("failed to listen grpc: %v", err)
	}
	grpcSrv := grpc.NewServer()
	pb.RegisterReplicationServer(grpcSrv, &grpcServer{st: st})
	go func() {
		log.Printf("Starting gRPC server on port %d...", *grpcPort)
		if err := grpcSrv.Serve(lis); err != nil {
			log.Fatalf("failed to serve grpc: %v", err)
		}
	}()

	api := &apiServer{st: st, role: *role}

	if *role == "primary" && *peerAddrs != "" {
		peers := strings.Split(*peerAddrs, ",")
		for _, addr := range peers {
			var conn *grpc.ClientConn
			for i := 0; i < 10; i++ {
				conn, err = grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
				if err == nil {
					break
				}
				log.Printf("Waiting for peer %s to be ready... (attempt %d)", addr, i+1)
				time.Sleep(2 * time.Second)
			}
			
			if err != nil {
				log.Printf("Failed to connect to peer %s after retries: %v", addr, err)
				continue
			}
			
			client := pb.NewReplicationClient(conn)
			api.backupPeers = append(api.backupPeers, client)
			log.Printf("Connected to backup peer: %s", addr)
		}
	}

	http.HandleFunc("/objects", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			api.handlePut(w, r)
		} else if r.Method == http.MethodGet {
			api.handleGet(w, r)
		}
	})
	http.HandleFunc("/objects/", api.handleGet)

	log.Printf("Node running as %s. HTTP on :%d, Data in %s", *role, *httpPort, *dataDir)
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", *httpPort), nil))
}