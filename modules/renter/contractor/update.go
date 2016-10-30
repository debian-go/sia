package contractor

import (
	"github.com/NebulousLabs/Sia/modules"
	"github.com/NebulousLabs/Sia/types"
)

// ProcessConsensusChange will be called by the consensus set every time there
// is a change in the blockchain. Updates will always be called in order.
func (c *Contractor) ProcessConsensusChange(cc modules.ConsensusChange) {
	c.mu.Lock()
	for _, block := range cc.RevertedBlocks {
		if block.ID() != types.GenesisID {
			c.blockHeight--
		}
	}
	for _, block := range cc.AppliedBlocks {
		if block.ID() != types.GenesisID {
			c.blockHeight++
		}
	}

	// delete expired contracts
	var expired []types.FileContractID
	for id, contract := range c.contracts {
		if c.blockHeight > contract.EndHeight() {
			// No need to wait for extra confirmations - any processes which
			// depend on this contract should have taken care of any issues
			// already.
			expired = append(expired, id)
		}
	}
	for _, id := range expired {
		delete(c.contracts, id)
		c.log.Debugln("INFO: deleted expired contract", id)
	}

	c.lastChange = cc.ID
	err := c.save()
	if err != nil {
		c.log.Println("Unable to save while processing a consensus change:", err)
	}
	c.mu.Unlock()

	// only attempt contract formation/renewal if we are synced
	// (harmless if not synced, since hosts will reject our renewal attempts,
	// (but very slow)
	if cc.Synced {
		go func() {
			// only one goroutine should be editing contracts at a time
			if !c.editLock.TryLock() {
				return
			}
			defer c.editLock.Unlock()

			// renew any contracts that have entered the renew window
			err := c.managedRenewContracts()
			if err != nil {
				c.log.Debugln("WARN: failed to renew contracts after processing a consensus chage:", err)
			}

			// if we don't have enough contracts, form new ones
			c.mu.RLock()
			a := c.allowance
			remaining := int(a.Hosts) - len(c.contracts)
			c.mu.RUnlock()
			if remaining <= 0 {
				return
			}
			numSectors, err := maxSectors(a, c.hdb, c.tpool)
			if err != nil {
				c.log.Debugln("ERROR: couldn't calculate maxSectors after processing a consensus change:", err)
				return
			}
			err = c.managedFormAllowanceContracts(remaining, numSectors, a)
			if err != nil {
				c.log.Debugln("WARN: failed to form contracts after processing a consensus change:", err)
			}
		}()
	}
}
