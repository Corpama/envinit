package configure

import (
	"fmt"
	"strconv"
	"strings"

	"envinit/internal/inventory"
)

var InventoryManagementColumns = []string{
	"hostname", "mgmt_mode", "mgmt_target_name", "mgmt_ip", "mgmt_prefix", "mgmt_gateway",
	"mgmt_iface1", "mgmt_mac1", "mgmt_iface2", "mgmt_mac2",
}

// inventoryRDMAFields are the ordinary per-slot planning fields. Route table
// and route CIDR are runtime-derived values and are intentionally omitted from
// newly initialized inventories. The parser still accepts those columns for
// compatibility with older or advanced inventories.
var inventoryRDMAFields = []string{"name", "ip", "prefix", "gateway", "mac", "rail_id"}

func inventorySlot(column string) int {
	if !strings.HasPrefix(column, "rdma") {
		return 0
	}
	n, _ := strconv.Atoi(strings.SplitN(strings.TrimPrefix(column, "rdma"), "_", 2)[0])
	return n
}

func InventorySlots(table *inventory.TemplateTable) int {
	count := 0
	for _, column := range table.Header {
		count = max(count, inventorySlot(column))
	}
	return count
}

// InitializeInventory fills only blank planning cells. It validates a copy,
// including the seed for future machines when the inventory has no rows.
func InitializeInventory(bundle []byte, source *inventory.TemplateTable, values map[string]string, slots int) (*inventory.TemplateTable, error) {
	if slots < 0 || slots > 64 {
		return nil, fmt.Errorf("RDMA 最大槽位数必须为 0..64")
	}
	for key := range values {
		switch key {
		case "mgmt_mode", "mgmt_target_name", "mgmt_prefix", "mgmt_gateway":
		default:
			return nil, fmt.Errorf("unsupported inventory initialization field %s", key)
		}
	}
	table := &inventory.TemplateTable{Warnings: append([]string(nil), source.Warnings...)}
	indices := []int{}
	for i, column := range source.Header {
		if inventorySlot(column) > slots {
			for row, cells := range source.Rows {
				if cells[i] != "" {
					return nil, fmt.Errorf("机器 %d 的 %s 已填写，不能缩减至 %d 个 RDMA 槽位", row+1, column, slots)
				}
			}
			continue
		}
		table.Header = append(table.Header, column)
		indices = append(indices, i)
	}
	for _, cells := range source.Rows {
		row := make([]string, len(indices))
		for i, index := range indices {
			row[i] = cells[index]
		}
		table.Rows = append(table.Rows, row)
	}
	ensure := func(column string) {
		for _, existing := range table.Header {
			if column == existing {
				return
			}
		}
		table.Header = append(table.Header, column)
		for i := range table.Rows {
			table.Rows[i] = append(table.Rows[i], "")
		}
	}
	for _, column := range InventoryManagementColumns {
		ensure(column)
	}
	for slot := 1; slot <= slots; slot++ {
		for _, field := range inventoryRDMAFields {
			ensure(fmt.Sprintf("rdma%d_%s", slot, field))
		}
	}
	seed := make([]string, len(table.Header))
	for column, key := range table.Header {
		seed[column] = strings.TrimSpace(values[key])
		for row := range table.Rows {
			if table.Rows[row][column] == "" {
				table.Rows[row][column] = seed[column]
			}
		}
	}
	// Validate defaults independently even if all existing cells are filled.
	probe := &inventory.TemplateTable{Header: table.Header, Rows: [][]string{seed}}
	if _, err := PrepareTemplate(bundle, probe); err != nil {
		return nil, err
	}
	if _, err := PrepareTemplate(bundle, table); err != nil {
		return nil, err
	}
	return table, nil
}
