package gocommerce

import (
	"context"
	"testing"
	"time"
)

func TestNewStoreIDIsAULIDAndSortsByTime(t *testing.T) {
	a, err := NewStoreID()
	if err != nil {
		t.Fatal(err)
	}
	if !ValidStoreID(a) || len(a) != len("store_")+26 {
		t.Fatalf("NewStoreID() = %q, want store_ + 26 Crockford characters", a)
	}
	time.Sleep(2 * time.Millisecond)
	b, _ := NewStoreID()
	if !(a < b) {
		t.Fatalf("ids do not sort by creation time: %q then %q", a, b)
	}
}

func TestValidStoreIDRefusesLookalikes(t *testing.T) {
	for _, s := range []string{"", "store_", "store_01JA2Z3K4M5N6P7Q8R9S0T1V2", "shop_01JA2Z3K4M5N6P7Q8R9S0T1V2W",
		"store_01JA2Z3K4M5N6P7Q8R9S0T1V2I", "store_01ja2z3k4m5n6p7q8r9s0t1v2w", "store_01JA2Z3K4M5N6P7Q8R9S0T1V2W'"} {
		if ValidStoreID(s) {
			t.Errorf("ValidStoreID(%q) = true", s)
		}
	}
}

func TestStoreTravelsInTheContext(t *testing.T) {
	if _, ok := StoreFrom(context.Background()); ok {
		t.Fatal("a bare context claims a store")
	}
	ctx := WithStore(context.Background(), "store_01JA2Z3K4M5N6P7Q8R9S0T1V2W")
	if got, ok := StoreFrom(ctx); !ok || got != "store_01JA2Z3K4M5N6P7Q8R9S0T1V2W" {
		t.Fatalf("StoreFrom = %q, %v", got, ok)
	}
}
