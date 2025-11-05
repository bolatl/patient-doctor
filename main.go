package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Patient struct {
	ID       int    `json:"id"`
	Login    string `json:"login"`
	Password string `json:"password"`
	Name     string `json:"name"`
}

type Doctor struct {
	ID       int    `json:"id"`
	Login    string `json:"login"`
	Password string `json:"password"`
	First    string `json:"first_name"`
	Last     string `json:"last_name"`
	Middle   string `json:"middle_name"`
	Special  string `json:"speciality"`
}

type Seed struct {
	Patients []Patient `json:"patients"`
	Doctors  []Doctor  `json:"doctors"`
}

type Selection struct {
	PatientID int `json:"patient_id"`
	DoctorID  int `json:"doctor_id"`
}

type LoginResp struct {
	Token string `json:"token"`
	Role  string `json:"role"` // "patient" | "doctor"
	ID    int    `json:"id"`
	Name  string `json:"name"`
}

type Store struct {
	mu       sync.RWMutex
	patients map[int]Patient
	doctors  map[int]Doctor
	byLogin  map[string]struct {
		role string
		id   int
		pass string
	}
	selections map[int]int
	subs       map[int]map[chan struct{}]struct{}

	selectionFile string
}

func NewStore(seedPath string) (*Store, error) {
	b, err := os.ReadFile(seedPath)
	if err != nil {
		return nil, err
	}
	var s Seed
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}

	st := &Store{
		patients: map[int]Patient{},
		doctors:  map[int]Doctor{},
		byLogin: map[string]struct {
			role string
			id   int
			pass string
		}{},
		selections:    map[int]int{},
		subs:          map[int]map[chan struct{}]struct{}{},
		selectionFile: filepath.Join(filepath.Dir(seedPath), "selections.json"),
	}
	for _, p := range s.Patients {
		st.patients[p.ID] = p
		st.byLogin["patient:"+p.Login] = struct {
			role string
			id   int
			pass string
		}{"patient", p.ID, p.Password}
	}
	for _, d := range s.Doctors {
		st.doctors[d.ID] = d
		st.byLogin["doctor:"+d.Login] = struct {
			role string
			id   int
			pass string
		}{"doctor", d.ID, d.Password}
		st.subs[d.ID] = map[chan struct{}]struct{}{}
	}
	_ = st.loadSelections()
	return st, nil
}

func (s *Store) loadSelections() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.selectionFile)
	if err != nil {
		return err
	}
	var arr []Selection
	if err := json.Unmarshal(b, &arr); err != nil {
		return err
	}
	for _, x := range arr {
		s.selections[x.PatientID] = x.DoctorID
	}
	return nil
}

func (s *Store) persistSelections() {
	s.mu.RLock()
	defer s.mu.RUnlock()
	arr := make([]Selection, 0, len(s.selections))
	for pid, did := range s.selections {
		arr = append(arr, Selection{PatientID: pid, DoctorID: did})
	}
	b, _ := json.MarshalIndent(arr, "", "  ")
	_ = os.WriteFile(s.selectionFile, b, 0644)
}

func (s *Store) Login(role, login, pass string) (LoginResp, error) {
	key := role + ":" + login
	entry, ok := s.byLogin[key]
	if !ok || entry.pass != pass {
		return LoginResp{}, errors.New("invalid credentials")
	}
	resp := LoginResp{
		Token: makeToken(),
		Role:  entry.role,
		ID:    entry.id,
	}
	if role == "patient" {
		resp.Name = s.patients[entry.id].Name
	} else {
		d := s.doctors[entry.id]
		resp.Name = strings.TrimSpace(d.Last + " " + d.First + " " + d.Middle)
	}
	return resp, nil
}

func (s *Store) GetPatient(id int) (Patient, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.patients[id]
	return p, ok
}

func (s *Store) GetDoctor(id int) (Doctor, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.doctors[id]
	return d, ok
}

func (s *Store) ListDoctors() []Doctor {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Doctor, 0, len(s.doctors))
	for _, d := range s.doctors {
		out = append(out, d)
	}
	return out
}

func (s *Store) SelectDoctor(patientID, doctorID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.patients[patientID]; !ok {
		return errors.New("patient not found")
	}
	if _, ok := s.doctors[doctorID]; !ok {
		return errors.New("doctor not found")
	}
	s.selections[patientID] = doctorID
	go s.persistSelections()
	for ch := range s.subs[doctorID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	return nil
}

func (s *Store) PatientsOfDoctor(doctorID int) []Patient {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var res []Patient
	for pid, did := range s.selections {
		if did == doctorID {
			if p, ok := s.patients[pid]; ok {
				res = append(res, p)
			}
		}
	}
	return res
}

func (s *Store) Subscribe(doctorID int) (ch chan struct{}, cancel func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch = make(chan struct{}, 1)
	if _, ok := s.subs[doctorID]; !ok {
		s.subs[doctorID] = map[chan struct{}]struct{}{}
	}
	s.subs[doctorID][ch] = struct{}{}
	cancel = func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.subs[doctorID], ch)
		close(ch)
	}
	return
}

func makeToken() string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	rand.Seed(time.Now().UnixNano())
	sb := strings.Builder{}
	for i := 0; i < 24; i++ {
		sb.WriteByte(letters[rand.Intn(len(letters))])
	}
	return sb.String()
}

func main() {
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.Dir("web")))
	store, err := NewStore(filepath.Join("data", "seed.json"))
	if err != nil {
		log.Fatalf("seed load: %v", err)
	}

	mux.HandleFunc("/api/ping", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"ok": "true"})
	})

	mux.HandleFunc("/api/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return
		}
		var body struct {
			Role     string `json:"role"`
			Login    string `json:"login"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", 400)
			return
		}
		resp, err := store.Login(body.Role, body.Login, body.Password)
		if err != nil {
			http.Error(w, "invalid credentials", 401)
			return
		}
		writeJSON(w, resp)
	})

	mux.HandleFunc("/api/patient/me", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.Atoi(r.URL.Query().Get("id"))
		p, ok := store.GetPatient(id)
		if !ok {
			http.Error(w, "not found", 404)
			return
		}
		store.mu.RLock()
		docID := store.selections[p.ID]
		store.mu.RUnlock()
		var doctor *Doctor
		if d, ok := store.GetDoctor(docID); ok {
			doctor = &d
		}
		writeJSON(w, map[string]any{"patient": p, "selected_doctor": doctor})
	})

	mux.HandleFunc("/api/doctors", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, store.ListDoctors())
	})

	mux.HandleFunc("/api/patient/select-doctor", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return
		}
		var body struct {
			PatientID int `json:"patient_id"`
			DoctorID  int `json:"doctor_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", 400)
			return
		}
		if err := store.SelectDoctor(body.PatientID, body.DoctorID); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("/api/doctor", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.Atoi(r.URL.Query().Get("id"))
		d, ok := store.GetDoctor(id)
		if !ok {
			http.Error(w, "not found", 404)
			return
		}
		pts := store.PatientsOfDoctor(id)
		writeJSON(w, map[string]any{"doctor": d, "patients": pts})
	})

	mux.HandleFunc("/api/doctor/stream", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.Atoi(r.URL.Query().Get("id"))
		if _, ok := store.GetDoctor(id); !ok {
			http.Error(w, "not found", 404)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		ch, cancel := store.Subscribe(id)
		defer cancel()

		ctx := r.Context()
		fmt.Fprintf(w, "event: ping\ndata: ok\n\n")
		flusher, _ := w.(http.Flusher)
		if flusher != nil {
			flusher.Flush()
		}

		for {
			select {
			case <-ctx.Done():
				return
			case <-ch:
				fmt.Fprintf(w, "event: update\ndata: changed\n\n")
				if flusher != nil {
					flusher.Flush()
				}
			case <-time.After(25 * time.Second):
				fmt.Fprintf(w, "event: ping\ndata: ok\n\n")
				if flusher != nil {
					flusher.Flush()
				}
			}
		}
	})

	addr := ":8080"
	log.Printf("server on %s", addr)
	srv := &http.Server{Addr: addr, Handler: withCORS(mux)}
	log.Fatal(srv.ListenAndServe())
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func shutdown(ctx context.Context, srv *http.Server) {
	_ = srv.Shutdown(ctx)
}
