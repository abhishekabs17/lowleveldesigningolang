package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ===== Domain =====

type VehicleType string

const (
	VehicleMotorcycle VehicleType = "MOTORCYCLE"
	VehicleCompact    VehicleType = "COMPACT"
	VehicleSedan      VehicleType = "SEDAN"
	VehicleSUV        VehicleType = "SUV"
	VehicleEV         VehicleType = "EV"
)

type SpotType string

const (
	SpotMotorcycle SpotType = "MOTORCYCLE"
	SpotCompact    SpotType = "COMPACT"
	SpotLarge      SpotType = "LARGE"
	SpotEV         SpotType = "EV"
	SpotHandicap   SpotType = "HANDICAP"
)

type SpotStatus string

const (
	SpotAvailable SpotStatus = "AVAILABLE"
	SpotOccupied  SpotStatus = "OCCUPIED"
	SpotOOS       SpotStatus = "OOS"
)

type Spot struct {
	ID      string
	LotID   string
	LevelID string
	Type    SpotType
	Status  SpotStatus
}

type Vehicle struct {
	Plate string
	Type  VehicleType
}

type TicketStatus string

const (
	TicketOpen   TicketStatus = "OPEN"
	TicketClosed TicketStatus = "CLOSED"
)

type Ticket struct {
	ID           string
	LotID        string
	SpotID       string
	VehiclePlate string
	VehicleType  VehicleType
	EntryAt      time.Time
	ExitAt       *time.Time
	Status       TicketStatus
	AmountDue    int64 // in cents/paise
	PricingVer   string
}

type PaymentStatus string

const (
	PaymentPending PaymentStatus = "PENDING"
	PaymentPaid    PaymentStatus = "PAID"
	PaymentFailed  PaymentStatus = "FAILED"
)

type Payment struct {
	ID        string
	TicketID  string
	Amount    int64
	Status    PaymentStatus
	Provider  string
	Receipt   string
	CreatedAt time.Time
}

// ===== Ports (Interfaces) =====

type SpotRepository interface {
	FindAvailable(ctx context.Context, lotID string, allowed []SpotType) (string, error) // returns SpotID
	Reserve(ctx context.Context, spotID string) error
	Release(ctx context.Context, spotID string) error
	Get(ctx context.Context, spotID string) (*Spot, error)
}

type TicketRepository interface {
	Create(ctx context.Context, t *Ticket) error
	Update(ctx context.Context, t *Ticket) error
	Get(ctx context.Context, id string) (*Ticket, error)
}

type PricingStrategy interface {
	Version() string
	// Calculate returns price in paise for [entry, exit)
	Calculate(entry, exit time.Time, sType SpotType, vType VehicleType) (int64, error)
}

type PaymentProvider interface {
	Charge(ctx context.Context, ticketID string, amount int64) (receipt string, err error)
	Name() string
}

// ===== Compatibility Matrix =====

var allowedMapping = map[VehicleType][]SpotType{
	VehicleMotorcycle: {SpotMotorcycle, SpotCompact, SpotLarge},
	VehicleCompact:    {SpotCompact, SpotLarge},
	VehicleSedan:      {SpotLarge, SpotCompact},
	VehicleSUV:        {SpotLarge},
	VehicleEV:         {SpotEV, SpotLarge},
}

// ===== Services =====

type AllocationService struct {
	spots SpotRepository
}

func NewAllocationService(spots SpotRepository) *AllocationService {
	return &AllocationService{spots: spots}
}

func (s *AllocationService) Allocate(ctx context.Context, lotID string, vt VehicleType) (spotID string, spotType SpotType, err error) {
	allowed := allowedMapping[vt]
	id, err := s.spots.FindAvailable(ctx, lotID, allowed)
	if err != nil {
		return "", "", err
	}
	if err := s.spots.Reserve(ctx, id); err != nil {
		return "", "", err
	}
	sp, err := s.spots.Get(ctx, id)
	if err != nil {
		_ = s.spots.Release(ctx, id) // best effort
		return "", "", err
	}
	return id, sp.Type, nil
}

func (s *AllocationService) Release(ctx context.Context, spotID string) error {
	return s.spots.Release(ctx, spotID)
}

type TicketService struct {
	tickets TicketRepository
	pricing PricingStrategy
}

func NewTicketService(t TicketRepository, p PricingStrategy) *TicketService {
	return &TicketService{tickets: t, pricing: p}
}

func (s *TicketService) Open(ctx context.Context, lotID, spotID string, v Vehicle) (*Ticket, error) {
	t := &Ticket{
		ID:           genID("tkt"),
		LotID:        lotID,
		SpotID:       spotID,
		VehiclePlate: v.Plate,
		VehicleType:  v.Type,
		EntryAt:      time.Now(),
		Status:       TicketOpen,
		PricingVer:   s.pricing.Version(),
	}
	if err := s.tickets.Create(ctx, t); err != nil {
		return nil, err
	}
	return t, nil
}

func (s *TicketService) CloseAndPrice(ctx context.Context, id string, spotType SpotType) (*Ticket, error) {
	t, err := s.tickets.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if t.Status != TicketOpen {
		return nil, errors.New("ticket not open")
	}
	now := time.Now()
	amt, err := s.pricing.Calculate(t.EntryAt, now, spotType, t.VehicleType)
	if err != nil {
		return nil, err
	}
	t.ExitAt = &now
	t.AmountDue = amt
	t.Status = TicketClosed
	if err := s.tickets.Update(ctx, t); err != nil {
		return nil, err
	}
	return t, nil
}

type PaymentService struct {
	provider PaymentProvider
}

func NewPaymentService(p PaymentProvider) *PaymentService {
	return &PaymentService{provider: p}
}

func (s *PaymentService) Pay(ctx context.Context, t *Ticket) (*Payment, error) {
	if t.Status != TicketClosed {
		return nil, errors.New("ticket must be closed before payment")
	}
	receipt, err := s.provider.Charge(ctx, t.ID, t.AmountDue)
	p := &Payment{
		ID:        genID("pay"),
		TicketID:  t.ID,
		Amount:    t.AmountDue,
		Provider:  s.provider.Name(),
		CreatedAt: time.Now(),
	}
	if err != nil {
		p.Status = PaymentFailed
		p.Receipt = ""
		return p, err
	}
	p.Status = PaymentPaid
	p.Receipt = receipt
	return p, nil
}

// ===== In-memory Adapters (thread-safe) =====

type inMemorySpotRepo struct {
	mu    sync.Mutex
	spots map[string]*Spot      // by ID
	byLot map[string][]string   // lot -> spotIDs
	index map[SpotType][]string // available IDs by type (naive list)
	occ   map[string]bool       // spotID -> reserved/occupied
}

func newInMemorySpotRepo(spots []*Spot) *inMemorySpotRepo {
	r := &inMemorySpotRepo{
		spots: make(map[string]*Spot),
		byLot: make(map[string][]string),
		index: make(map[SpotType][]string),
		occ:   make(map[string]bool),
	}
	for _, s := range spots {
		cp := *s
		r.spots[s.ID] = &cp
		r.byLot[s.LotID] = append(r.byLot[s.LotID], s.ID)
		if s.Status == SpotAvailable {
			r.index[s.Type] = append(r.index[s.Type], s.ID)
		}
	}
	return r
}

func (r *inMemorySpotRepo) Get(ctx context.Context, spotID string) (*Spot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.spots[spotID]
	if !ok {
		return nil, errors.New("spot not found")
	}
	cp := *s
	return &cp, nil
}

func (r *inMemorySpotRepo) FindAvailable(ctx context.Context, lotID string, allowed []SpotType) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	allowedSet := make(map[SpotType]struct{})
	for _, a := range allowed {
		allowedSet[a] = struct{}{}
	}
	for st, ids := range r.index {
		if _, ok := allowedSet(st); !ok {
			continue
		}
		// find first available in this type and lot
		for i, id := range ids {
			s := r.spots[id]
			if s.LotID != lotID || s.Status != SpotAvailable || r.occ[id] {
				continue
			}
			// optimistic pick; keep in index, we mark reserved on Reserve
			return id, nil
		}
	}
	return "", errors.New("no available spot")
}

func (r *inMemorySpotRepo) Reserve(ctx context.Context, spotID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.spots[spotID]
	if !ok {
		return errors.New("spot not found")
	}
	if s.Status != SpotAvailable || r.occ[spotID] {
		return errors.New("spot not available")
	}
	r.occ[spotID] = true
	s.Status = SpotOccupied
	return nil
}

func (r *inMemorySpotRepo) Release(ctx context.Context, spotID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.spots[spotID]
	if !ok {
		return errors.New("spot not found")
	}
	r.occ[spotID] = false
	s.Status = SpotAvailable
	return nil
}

type inMemoryTicketRepo struct {
	mu      sync.Mutex
	tickets map[string]*Ticket
}

func newInMemoryTicketRepo() *inMemoryTicketRepo {
	return &inMemoryTicketRepo{tickets: make(map[string]*Ticket)}
}

func (r *inMemoryTicketRepo) Create(ctx context.Context, t *Ticket) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tickets[t.ID]; exists {
		return errors.New("duplicate ticket")
	}
	cp := *t
	r.tickets[t.ID] = &cp
	return nil
}

func (r *inMemoryTicketRepo) Update(ctx context.Context, t *Ticket) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tickets[t.ID]; !exists {
		return errors.New("not found")
	}
	cp := *t
	r.tickets[t.ID] = &cp
	return nil
}

func (r *inMemoryTicketRepo) Get(ctx context.Context, id string) (*Ticket, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tickets[id]
	if !ok {
		return nil, errors.New("not found")
	}
	cp := *t
	return &cp, nil
}

// ===== Pricing Strategies =====

// Simple tiered: first hour flat 50, then 30 per started hour
type TieredPricing struct{ version string }

func NewTieredPricing(version string) *TieredPricing { return &TieredPricing{version: version} }
func (p *TieredPricing) Version() string             { return p.version }
func (p *TieredPricing) Calculate(entry, exit time.Time, sType SpotType, vType VehicleType) (int64, error) {
	if !exit.After(entry) {
		return 0, errors.New("invalid time range")
	}
	dur := exit.Sub(entry)
	hours := int64(dur.Hours())
	if dur%time.Hour != 0 {
		hours++ // round up
	}
	base := int64(5000) // 50.00
	if hours <= 1 {
		return base, nil
	}
	rest := (hours - 1) * 3000 // 30.00 per extra hour
	// small surcharges
	switch sType {
	case SpotEV, SpotHandicap:
		rest += 1000
	case SpotLarge:
		rest += 500
	}
	return base + rest, nil
}

// ===== Payment Provider (mock) =====

type mockProvider struct{ name string }

func (m *mockProvider) Charge(ctx context.Context, ticketID string, amount int64) (string, error) {
	// Always succeeds in demo
	return fmt.Sprintf("RCP-%s-%d", ticketID, amount), nil
}
func (m *mockProvider) Name() string { return m.name }

// ===== Utilities =====

var idMu sync.Mutex
var idSeq int64

func genID(prefix string) string {
	idMu.Lock()
	defer idMu.Unlock()
	idSeq++
	return fmt.Sprintf("%s-%d", prefix, idSeq)
}

// ===== Demo main =====

func main() {
	ctx := context.Background()

	// Inventory: 2 levels, mixed spots
	spots := []*Spot{
		{ID: "S1", LotID: "LOT-1", LevelID: "L1", Type: SpotCompact, Status: SpotAvailable},
		{ID: "S2", LotID: "LOT-1", LevelID: "L1", Type: SpotLarge, Status: SpotAvailable},
		{ID: "S3", LotID: "LOT-1", LevelID: "L2", Type: SpotEV, Status: SpotAvailable},
		{ID: "S4", LotID: "LOT-1", LevelID: "L2", Type: SpotMotorcycle, Status: SpotAvailable},
	}

	spotRepo := newInMemorySpotRepo(spots)
	ticketRepo := newInMemoryTicketRepo()
	pricing := NewTieredPricing("v1.0")
	paymentProv := &mockProvider{name: "MockPay"}

	alloc := NewAllocationService(spotRepo)
	tickets := NewTicketService(ticketRepo, pricing)
	payments := NewPaymentService(paymentProv)

	// Entry: EV vehicle arrives
	spotID, sType, err := alloc.Allocate(ctx, "LOT-1", VehicleEV)
	if err != nil {
		panic(err)
	}
	tkt, err := tickets.Open(ctx, "LOT-1", spotID, Vehicle{Plate: "DL01AB1234", Type: VehicleEV})
	if err != nil {
		panic(err)
	}
	fmt.Printf("ENTRY OK: ticket=%s spot=%s (%s)\n", tkt.ID, spotID, sType)

	// Sleep to simulate parking time
	time.Sleep(1500 * time.Millisecond) // ~1.5s -> rounds to 1 hour in demo

	// Exit: close ticket & price
	closed, err := tickets.CloseAndPrice(ctx, tkt.ID, sType)
	if err != nil {
		panic(err)
	}
	fmt.Printf("EXIT OK: ticket=%s amount_due=%.2f\n", closed.ID, float64(closed.AmountDue)/100.0)

	// Payment
	pmt, err := payments.Pay(ctx, closed)
	if err != nil {
		fmt.Printf("PAYMENT FAILED: %v\n", err)
	} else {
		fmt.Printf("PAYMENT OK: receipt=%s provider=%s\n", pmt.Receipt, pmt.Provider)
	}

	// Release the spot
	if err := alloc.Release(ctx, spotID); err != nil {
		panic(err)
	}
	fmt.Println("SPOT RELEASED.")
}
