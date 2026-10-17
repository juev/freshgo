package authguard

import (
	"sync"

	"golang.org/x/crypto/bcrypt"
)

// Password reports whether the password is the one the hash is of.
func Password(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// Cost is what bcrypt spends on the hash, its default cost when the hash is
// none.
func Cost(hash string) int {
	if cost, err := bcrypt.Cost([]byte(hash)); err == nil {
		return cost
	}
	return bcrypt.DefaultCost
}

// Decoys are hashes no password matches, by bcrypt cost: comparing against
// one takes as long as comparing against the hash of an account.
//
// The zero value is ready. A Decoys belongs to one handler; what it holds is
// a cache, not a counter.
type Decoys struct {
	mu     sync.Mutex
	byCost map[int][]byte
}

// Refuse answers that the password is not the one, after as much work as
// comparing against a hash of the given cost. It is what a refusal does when
// there is no hash to compare: a name without an account, an account
// without a password, a disabled one.
func (d *Decoys) Refuse(password string, cost int) {
	_ = bcrypt.CompareHashAndPassword(d.withCost(cost), []byte(password))
}

// Costs reports which costs the decoys are held for, for tests.
func (d *Decoys) Costs() []int {
	d.mu.Lock()
	defer d.mu.Unlock()
	costs := make([]int, 0, len(d.byCost))
	for cost := range d.byCost {
		costs = append(costs, cost)
	}
	return costs
}

func (d *Decoys) withCost(cost int) []byte {
	if cost < bcrypt.MinCost || cost > bcrypt.MaxCost {
		cost = bcrypt.DefaultCost
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if hash, ok := d.byCost[cost]; ok {
		return hash
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("nobody has this password"), cost)
	if err != nil {
		// Nothing compares equal to no hash at all, only faster.
		return nil
	}
	if d.byCost == nil {
		d.byCost = map[int][]byte{}
	}
	d.byCost[cost] = hash
	return hash
}
