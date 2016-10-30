package contractor

import (
	"os"
	"testing"

	"github.com/NebulousLabs/Sia/build"
	"github.com/NebulousLabs/Sia/modules"
	"github.com/NebulousLabs/Sia/types"
)

// memPersist implements the persister interface in-memory.
type memPersist contractorPersist

func (m *memPersist) save(data contractorPersist) error     { *m = memPersist(data); return nil }
func (m *memPersist) saveSync(data contractorPersist) error { *m = memPersist(data); return nil }
func (m memPersist) load(data *contractorPersist) error     { *data = contractorPersist(m); return nil }

// TestSaveLoad tests that the contractor can save and load itself.
func TestSaveLoad(t *testing.T) {
	// create contractor with mocked persist dependency
	c := &Contractor{
		contracts:  make(map[types.FileContractID]modules.RenterContract),
		renewedIDs: make(map[types.FileContractID]types.FileContractID),
	}
	c.persist = new(memPersist)

	// add some fake contracts
	c.contracts = map[types.FileContractID]modules.RenterContract{
		{0}: {NetAddress: "foo"},
		{1}: {NetAddress: "bar"},
		{2}: {NetAddress: "baz"},
	}
	c.renewedIDs = map[types.FileContractID]types.FileContractID{
		{0}: {1},
		{1}: {2},
		{2}: {3},
	}

	// save and reload
	err := c.save()
	if err != nil {
		t.Fatal(err)
	}
	err = c.load()
	if err != nil {
		t.Fatal(err)
	}
	// check that contracts were restored
	_, ok0 := c.contracts[types.FileContractID{0}]
	_, ok1 := c.contracts[types.FileContractID{1}]
	_, ok2 := c.contracts[types.FileContractID{2}]
	if !ok0 || !ok1 || !ok2 {
		t.Fatal("contracts were not restored properly:", c.contracts)
	}
	_, ok0 = c.renewedIDs[types.FileContractID{0}]
	_, ok1 = c.renewedIDs[types.FileContractID{1}]
	_, ok2 = c.renewedIDs[types.FileContractID{2}]
	if !ok0 || !ok1 || !ok2 {
		t.Fatal("renewed IDs were not restored properly:", c.renewedIDs)
	}

	// use stdPersist instead of mock
	c.persist = newPersist(build.TempDir("contractor", "TestSaveLoad"))
	os.MkdirAll(build.TempDir("contractor", "TestSaveLoad"), 0700)

	// save and reload
	err = c.save()
	if err != nil {
		t.Fatal(err)
	}
	err = c.load()
	if err != nil {
		t.Fatal(err)
	}
	// check that contracts were restored
	_, ok0 = c.contracts[types.FileContractID{0}]
	_, ok1 = c.contracts[types.FileContractID{1}]
	_, ok2 = c.contracts[types.FileContractID{2}]
	if !ok0 || !ok1 || !ok2 {
		t.Fatal("contracts were not restored properly:", c.contracts)
	}
	_, ok0 = c.renewedIDs[types.FileContractID{0}]
	_, ok1 = c.renewedIDs[types.FileContractID{1}]
	_, ok2 = c.renewedIDs[types.FileContractID{2}]
	if !ok0 || !ok1 || !ok2 {
		t.Fatal("renewed IDs were not restored properly:", c.renewedIDs)
	}
}
