package subnet

import (
	address "github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/filecoin-project/go-state-types/exitcode"
	"github.com/filecoin-project/lotus/chain/consensus/hierarchical"
	"github.com/filecoin-project/lotus/chain/consensus/hierarchical/checkpoints/schema"
	"github.com/filecoin-project/lotus/chain/consensus/hierarchical/checkpoints/types"
	"github.com/filecoin-project/specs-actors/v3/actors/builtin"
	"github.com/filecoin-project/specs-actors/v6/actors/runtime"
	"github.com/filecoin-project/specs-actors/v6/actors/util/adt"
	cid "github.com/ipfs/go-cid"
	"golang.org/x/xerrors"
)

const (
	// SignatureThreshold that determines the number of votes from
	// total number of miners expected to propagate a checkpoint to
	// SCA
	SignatureThreshold = float32(0.66)
)

var (
	// MinSubnetStake required to create a new subnet
	MinSubnetStake = abi.NewTokenAmount(1e18)

	// MinMinerStake is the minimum take required for a
	// miner to be granted mining rights in the subnet and join it.
	MinMinerStake = abi.NewTokenAmount(1e18)

	// LeavingFee Penalization
	// Coefficient divided to miner stake when leaving a subnet.
	// NOTE: This is currently set to 1, i.e., the miner recovers
	// its full stake. This may change once cryptoecon is figured out.
	// We'll need to decide what to do with the leftover stake, if to
	// burn it or keep it until the subnet is full killed.
	LeavingFeeCoeff = big.NewInt(1)
)

// ConsensusType for subnet
type ConsensusType uint64

// List of supported/implemented consensus for subnets.
const (
	Delegated ConsensusType = iota
	PoW
)

// SubnetStatus describes in what state in its lifecycle a subnet is.
type Status uint64

const (
	Instantiated Status = iota // Waiting to onboard minimum stake to register in SCA
	Active                     // Active and operating
	Inactive                   // Inactive for lack of stake
	Terminating                // Waiting for everyone to take their funds back and close the subnet
	Killed                     // Not active anymore.

)

type SubnetState struct {
	Name      string
	ParentID  hierarchical.SubnetID
	Consensus ConsensusType
	// Minimum stake required by new joiners.
	MinMinerStake abi.TokenAmount
	// NOTE: Consider adding miners list as AMT
	Miners     []address.Address
	TotalStake abi.TokenAmount
	Stake      cid.Cid // BalanceTable with the distribution of stake by miners
	// State of the subnet
	Status Status
	// Genesis bootstrap for the subnet. This is created
	// when the subnet is generated.
	Genesis     []byte
	CheckPeriod abi.ChainEpoch
	// Checkpoints submit to SubnetActor per epoch
	Checkpoints cid.Cid // HAMT[epoch]Checkpoint
	// WindowChecks
	WindowChecks cid.Cid // HAMT[cid]CheckVotes
}

type CheckVotes struct {
	// NOTE: I don't think we need to store the checkpoint for anything.
	// By keeping the Cid of the checkpoint as the key is enough and we
	// save space
	// Checkpoint schema.Checkpoint
	Miners []address.Address
}

func (st SubnetState) majorityVote(wch *CheckVotes) bool {
	return float32(len(wch.Miners))/float32(len(st.Miners)) >= SignatureThreshold
}
func ConstructSubnetState(store adt.Store, params *ConstructParams) (*SubnetState, error) {
	emptyStakeCid, err := adt.StoreEmptyMap(store, adt.BalanceTableBitwidth)
	if err != nil {
		return nil, xerrors.Errorf("failed to create stakes balance table: %w", err)
	}
	emptyCheckpointsMapCid, err := adt.StoreEmptyMap(store, builtin.DefaultHamtBitwidth)
	if err != nil {
		return nil, xerrors.Errorf("failed to create empty map: %w", err)
	}

	/* Initialize AMT of miners.
	emptyArr, err := adt.MakeEmptyArray(adt.AsStore(rt), LaneStatesAmtBitwidth)
	builtin.RequireNoErr(rt, err, exitcode.ErrIllegalState, "failed to create empty array")
	emptyArrCid, err := emptyArr.Root()
	builtin.RequireNoErr(rt, err, exitcode.ErrIllegalState, "failed to persist empty array")
	*/

	parentID := hierarchical.SubnetID(params.NetworkName)

	st := &SubnetState{
		ParentID:      parentID,
		Consensus:     params.Consensus,
		MinMinerStake: params.MinMinerStake,
		Miners:        make([]address.Address, 0),
		Stake:         emptyStakeCid,
		Status:        Instantiated,
		CheckPeriod:   params.CheckPeriod,
		Checkpoints:   emptyCheckpointsMapCid,
	}

	err = st.emptyWindowChecks(store)
	if err != nil {
		return nil, err
	}

	return st, nil
}

func (st *SubnetState) emptyWindowChecks(store adt.Store) error {
	var err error
	st.WindowChecks, err = adt.StoreEmptyMap(store, builtin.DefaultHamtBitwidth)
	return err
}

// windowCheckpoint returns the checkpoint for the current signing window (if any).
func (st *SubnetState) epochCheckpoint(rt runtime.Runtime) (*schema.Checkpoint, bool, error) {
	chEpoch := types.CheckpointEpoch(rt.CurrEpoch(), st.CheckPeriod)
	return st.GetCheckpoint(adt.AsStore(rt), chEpoch)
}

// prevCheckCid returns the Cid of the previously committed checkpoint
func (st *SubnetState) prevCheckCid(rt runtime.Runtime) (cid.Cid, error) {
	chEpoch := types.CheckpointEpoch(rt.CurrEpoch(), st.CheckPeriod)
	ep := chEpoch - st.CheckPeriod
	// If we are in the first period.
	if ep < 0 {
		return schema.NoPreviousCheck, nil
	}
	ch, found, err := st.GetCheckpoint(adt.AsStore(rt), ep)
	if err != nil {
		return cid.Undef, err
	}
	if !found {
		// TODO: We could optionally return an error here.
		return schema.NoPreviousCheck, nil
	}
	return ch.Cid()
}

// GetCheckpoint gets a checkpoint from its index
func (st *SubnetState) GetCheckpoint(s adt.Store, epoch abi.ChainEpoch) (*schema.Checkpoint, bool, error) {
	checkpoints, err := adt.AsMap(s, st.Checkpoints, builtin.DefaultHamtBitwidth)
	if err != nil {
		return nil, false, xerrors.Errorf("failed to load checkpoint: %w", err)
	}
	return getCheckpoint(checkpoints, epoch)
}

func getCheckpoint(checkpoints *adt.Map, epoch abi.ChainEpoch) (*schema.Checkpoint, bool, error) {
	var out schema.Checkpoint
	found, err := checkpoints.Get(abi.UIntKey(uint64(epoch)), &out)
	if err != nil {
		return nil, false, xerrors.Errorf("failed to get checkpoint for epoch %v: %w", epoch, err)
	}
	if !found {
		return nil, false, nil
	}
	return &out, true, nil
}

func (st *SubnetState) flushCheckpoint(rt runtime.Runtime, ch *schema.Checkpoint) {
	// Update subnet in the list of checkpoints.
	checks, err := adt.AsMap(adt.AsStore(rt), st.Checkpoints, builtin.DefaultHamtBitwidth)
	builtin.RequireNoErr(rt, err, exitcode.ErrIllegalState, "failed to load state for checkpoints")
	err = checks.Put(abi.UIntKey(uint64(ch.Data.Epoch)), ch)
	builtin.RequireNoErr(rt, err, exitcode.ErrIllegalState, "failed to put checkpoint in map")
	// Flush checkpoints
	st.Checkpoints, err = checks.Root()
	builtin.RequireNoErr(rt, err, exitcode.ErrIllegalState, "failed to flush checkpoints")
}

func (st *SubnetState) GetWindowChecks(s adt.Store, checkCid cid.Cid) (*CheckVotes, bool, error) {
	checks, err := adt.AsMap(s, st.WindowChecks, builtin.DefaultHamtBitwidth)
	if err != nil {
		return nil, false, xerrors.Errorf("failed to load windowCheck: %w", err)
	}

	var out CheckVotes
	found, err := checks.Get(abi.CidKey(checkCid), &out)
	if err != nil {
		return nil, false, xerrors.Errorf("failed to get windowCheck for Cid %v: %w", checkCid, err)
	}
	if !found {
		return nil, false, nil
	}
	return &out, true, nil
}

func (st *SubnetState) flushWindowChecks(rt runtime.Runtime, checkCid cid.Cid, w *CheckVotes) {
	checks, err := adt.AsMap(adt.AsStore(rt), st.WindowChecks, builtin.DefaultHamtBitwidth)
	builtin.RequireNoErr(rt, err, exitcode.ErrIllegalState, "failed to load state for windowChecks")
	err = checks.Put(abi.CidKey(checkCid), w)
	builtin.RequireNoErr(rt, err, exitcode.ErrIllegalState, "failed to put windowCheck in map")
	// Flush windowCheck
	st.WindowChecks, err = checks.Root()
	builtin.RequireNoErr(rt, err, exitcode.ErrIllegalState, "failed to flush windowChecks")
}

func (st *SubnetState) IsMiner(addr address.Address) bool {
	return hasMiner(addr, st.Miners)
}

func hasMiner(addr address.Address, miners []address.Address) bool {
	for _, a := range miners {
		if a == addr {
			return true
		}
	}
	return false
}
