package configure

import (
	"strings"
	"testing"

	"envinit/internal/inventory"
)

func TestInventoryInitialization(t *testing.T) {
	bundle := []byte(`{"platform":{"os_family":"ubuntu"}}`)
	source, err := inventory.ParseTemplate([]byte("hostname,mgmt_prefix,Owner\na,25,  team  \nb,,other\n"))
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{"mgmt_mode": "bond", "mgmt_target_name": "bond0", "mgmt_prefix": "24"}
	table, err := InitializeInventory(bundle, source, values, 4)
	if err != nil {
		t.Fatal(err)
	}
	if InventorySlots(table) != 4 || len(table.Header) != 35 || table.Rows[0][1] != "25" || table.Rows[1][1] != "24" || table.Rows[0][2] != "  team  " {
		t.Fatalf("initialization lost values or headers: %+v", table)
	}
	if len(source.Header) != 3 || source.Rows[1][1] != "" {
		t.Fatal("initialization mutated its source")
	}
	table.Set(0, "rdma4_ip", "10.0.0.2")
	if _, err := InitializeInventory(bundle, table, values, 2); err == nil {
		t.Fatal("shrinking discarded a planned interface")
	}
	table.Set(0, "rdma4_ip", "")
	shrunk, err := InitializeInventory(bundle, table, values, 2)
	if err != nil || InventorySlots(shrunk) != 2 {
		t.Fatalf("cannot shrink empty slots: %v", err)
	}
	empty, _ := inventory.ParseTemplate([]byte("hostname\n"))
	initialized, err := InitializeInventory(bundle, empty, values, 8)
	if err != nil || len(initialized.Rows) != 0 || !strings.Contains(string(initialized.CSV()), "rdma8_prefix") {
		t.Fatalf("empty template initialization: %v", err)
	}
	for key, value := range map[string]string{"mgmt_mode": "wrong", "mgmt_prefix": "33", "mgmt_target_name": "bad name"} {
		if _, err := InitializeInventory(bundle, empty, map[string]string{key: value}, 0); err == nil {
			t.Fatalf("invalid seed accepted on empty template: %s", key)
		}
	}
	for _, count := range []int{-1, 65} {
		if _, err := InitializeInventory(bundle, empty, nil, count); err == nil {
			t.Fatalf("invalid slot count accepted: %d", count)
		}
	}
}
