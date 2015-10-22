// rxmachine
package lacp

import (
	"fmt"
	"time"
	"utils/fsm"
)

// rxm states
/*
const LacpRxmNone = "LacpRxmNone"    // not apart of the state machine, but used as an initial state
const LacpRxmInitialize = "LacpRxmInitialize"
const LacpRxmPortDisable = "LacpRxmPortDisable"
const LacpRxmExpired = "LacpRxmExpired"
const LacpRxmLacpDisabled = "LacpRxmLacpDisabled"
const LacpRxmDefaulted = "LacpRxmDefaulted"
const LacpRxmCurrent = "LacpRxmCurrent"
*/
const (
	LacpRxmStateNone = iota + 1
	LacpRxmStateInitialize
	LacpRxmStatePortDisabled
	LacpRxmStateExpired
	LacpRxmStateLacpDisabled
	LacpRxmStateDefaulted
	LacpRxmStateCurrent
)

// rxm events
/*
const LacpRxmBeginEvent = "LacpRxmBeginEvent"
const LacpRxmPortMovedEvent = "LacpRxmPortMovedEvent"
const LacpRxmCurrentWhileTimerExpiredEvent = "LacpRxmCurrentWhileTimerExpiredEvent"
const LacpRxmLacpDisabledEvent = "LacpRxmLacpDisabledEvent"
const LacpRxmLacpEnabledEvent = "LacpRxmLacpEnabledEvent"
const LacpRxmLacpPortDisabledEvent = "LacpRxmLacpPortDisabledEvent"
const LacpRxmLacpPktRxEvent = "LacpRxmLacpPktRxEvent"
*/
const (
	LacpRxmEventBegin = iota + 1
	LacpRxmEventUnconditionalFallthrough
	LacpRxmEventNotPortEnabledAndNotPortMoved
	LacpRxmEventPortMoved
	LacpRxmEventPortEnabledAndLacpEnabled
	LacpRxmEventPortEnabledAndLacpDisabled
	LacpRxmEventCurrentWhileTimerExpired
	LacpRxmEventLacpEnabled
	LacpRxmEventLacpPktRx
	LacpRxmEventKillSignal
)

// LacpRxMachine holds FSM and current state
// and event channels for state transitions
type LacpRxMachine struct {
	// for debugging
	PreviousState fsm.State

	Machine *fsm.Machine

	p *LaAggPort

	// debug log
	log    chan string
	logEna bool

	// timer interval
	currentWhileTimerTimeout time.Duration

	// timers
	currentWhileTimer *time.Timer

	// machine specific events
	RxmEvents          chan fsm.Event
	RxmPktRxEvent      chan LacpPdu
	RxmKillSignalEvent chan bool
	RxmLogEnableEvent  chan bool
}

func (rxm *LacpRxMachine) PrevState() fsm.State { return rxm.PreviousState }

// PrevStateSet will set the previous state
func (rxm *LacpRxMachine) PrevStateSet(s fsm.State) { rxm.PreviousState = s }

// Stop should clean up all resources
func (rxm *LacpRxMachine) Stop() {
	rxm.CurrentWhileTimerStop()

	// stop the go routine
	rxm.RxmKillSignalEvent <- true

	close(rxm.RxmEvents)
	close(rxm.RxmPktRxEvent)
	close(rxm.RxmKillSignalEvent)
	close(rxm.RxmLogEnableEvent)

}

// NewLacpRxMachine will create a new instance of the LacpRxMachine
func NewLacpRxMachine(port *LaAggPort) *LacpRxMachine {
	rxm := &LacpRxMachine{
		p:                  port,
		log:                port.LacpDebug.LacpLogChan,
		logEna:             true,
		PreviousState:      LacpRxmStateNone,
		RxmEvents:          make(chan fsm.Event),
		RxmPktRxEvent:      make(chan LacpPdu),
		RxmKillSignalEvent: make(chan bool),
		RxmLogEnableEvent:  make(chan bool)}

	port.rxMachineFsm = rxm

	return rxm
}

func (rxm *LacpRxMachine) LacpRxmLog(msg string) {
	if rxm.logEna {
		rxm.log <- msg
	}
}

// A helpful function that lets us apply arbitrary rulesets to this
// instances state machine without reallocating the machine.
func (rxm *LacpRxMachine) Apply(r *fsm.Ruleset) *fsm.Machine {
	if rxm.Machine == nil {
		rxm.Machine = &fsm.Machine{}
	}

	// Assign the ruleset to be used for this machine
	rxm.Machine.Rules = r

	return rxm.Machine
}

// LacpRxMachineInitialize function to be called after
// state transition to INITIALIZE
func (rxm *LacpRxMachine) LacpRxMachineInitialize(m fsm.Machine, data interface{}) fsm.State {
	rxm.LacpRxmLog("RXM: Initialize Enter")

	p := rxm.p

	// detach from aggregator
	p.aggId = 0

	// make sure no timer is running
	p.muxMachineFsm.WaitWhileTimerStop()

	// set the agg as being unselected
	p.aggSelected = LacpAggUnSelected

	// clear the Expired Bit
	LacpStateClear(p.actorOper.state, LacpStateExpiredBit)

	// set the port moved to false
	p.portMoved = false

	// next state
	return LacpRxmStateInitialize
}

// LacpRxMachineExpired function to be called after
// state transition to PORT_DISABLED
func (rxm *LacpRxMachine) LacpRxMachinePortDisabled(m fsm.Machine, data interface{}) fsm.State {
	rxm.LacpRxmLog("RXM: Port Disable Enter")
	p := rxm.p

	// Clear the Sync state bit
	LacpStateClear(p.partnerOper.state, LacpStateSyncBit)

	return LacpRxmStatePortDisabled
}

// LacpRxMachineExpired function to be called after
// state transition to EXPIRED
func (rxm *LacpRxMachine) LacpRxMachineExpired(m fsm.Machine, data interface{}) fsm.State {
	rxm.LacpRxmLog("RXM: Expired Enter")
	p := rxm.p

	// Sync set to FALSE
	LacpStateClear(p.partnerOper.state, LacpStateSyncBit)

	// Short timeout
	LacpStateSet(p.partnerOper.state, LacpStateTimeoutBit)

	// Set the Short timeout
	rxm.CurrentWhileTimerTimeoutSet(LacpShortTimeoutTime)

	// Start the Current While timer
	rxm.CurrentWhileTimerStart()

	// Actor Oper Port State Expired = TRUE
	LacpStateSet(p.actorOper.state, LacpStateExpiredBit)

	return LacpRxmStateExpired
}

// LacpRxMachineLacpDisabled function to be called after
// state transition to LACP_DISABLED
func (rxm *LacpRxMachine) LacpRxMachineLacpDisabled(m fsm.Machine, data interface{}) fsm.State {
	rxm.LacpRxmLog("RXM: Lacp Disabled Enter")
	p := rxm.p

	// Unselect the aggregator
	p.aggSelected = LacpAggUnSelected

	// setup the default params
	rxm.recordDefault()

	// clear the Aggregation bit
	LacpStateClear(p.partnerOper.state, LacpStateAggregationBit)

	// clear the expired bit
	LacpStateClear(p.actorOper.state, LacpStateExpiredBit)

	return LacpRxmStateLacpDisabled
}

// LacpRxMachineDefaulted function to be called after
// state transition to DEFAULTED
func (rxm *LacpRxMachine) LacpRxMachineDefaulted(m fsm.Machine, data interface{}) fsm.State {
	rxm.LacpRxmLog("RXM: Defaulted Enter")
	p := rxm.p

	//lacpPduInfo := data.(LacpPdu)

	// Updated the default selected state
	rxm.updateDefaultSelected()

	// Record the default partner info
	rxm.recordDefault()

	// Clear the expired bit on the actor oper state
	LacpStateClear(p.actorOper.state, LacpStateExpiredBit)

	return LacpRxmStateDefaulted
}

// LacpRxMachineCurrent function to be called after
// state transition to CURRENT
func (rxm *LacpRxMachine) LacpRxMachineCurrent(m fsm.Machine, data interface{}) fsm.State {
	rxm.LacpRxmLog("RXM: Current Enter")

	p := rxm.p

	// Version 1, V2 will require a serialize/deserialize routine since TLV's are involved
	lacpPduInfo := data.(LacpPdu)

	// update selection logic
	rxm.updateSelected(&lacpPduInfo)

	// update the ntt
	ntt := rxm.updateNTT(&lacpPduInfo)

	// Version 2 or higher check
	if LacpActorSystemLacpVersion >= 0x2 {
		rxm.recordVersionNumber(&lacpPduInfo)
	}

	// record the current packet state
	rxm.recordPDU(&lacpPduInfo)

	// lets kick off the Current While Timer
	rxm.CurrentWhileTimerStart()

	// Actor_Oper_Port_Sate.Expired = FALSE
	LacpStateClear(p.actorOper.state, LacpStateExpiredBit)

	if ntt == true {
		// update ntt, which should trigger a packet transmit
		p.txMachineFsm.TxmEvents <- LacpTxmEventNtt
	}

	return LacpRxmStateCurrent
}

func LacpRxMachineFSMBuild(p *LaAggPort) *LacpRxMachine {

	rules := fsm.Ruleset{}

	// Instantiate a new LacpRxMachine
	// Initial state will be a psuedo state known as "begin" so that
	// we can transition to the initalize state
	rxm := NewLacpRxMachine(p)

	//BEGIN -> INIT
	rules.AddRule(LacpRxmStateNone, LacpRxmEventBegin, rxm.LacpRxMachineInitialize)
	rules.AddRule(LacpRxmStatePortDisabled, LacpRxmEventBegin, rxm.LacpRxMachineInitialize)
	rules.AddRule(LacpRxmStateLacpDisabled, LacpRxmEventBegin, rxm.LacpRxMachineInitialize)
	rules.AddRule(LacpRxmStateDefaulted, LacpRxmEventBegin, rxm.LacpRxMachineInitialize)
	rules.AddRule(LacpRxmStateCurrent, LacpRxmEventBegin, rxm.LacpRxMachineInitialize)
	// INIT -> PORT_DISABLE
	rules.AddRule(LacpRxmStateInitialize, LacpRxmEventUnconditionalFallthrough, rxm.LacpRxMachinePortDisabled)
	// NOT PORT ENABLED  && NOT PORT MOVED
	// All states transition to this state
	rules.AddRule(LacpRxmStateExpired, LacpRxmEventNotPortEnabledAndNotPortMoved, rxm.LacpRxMachinePortDisabled)
	rules.AddRule(LacpRxmStateLacpDisabled, LacpRxmEventNotPortEnabledAndNotPortMoved, rxm.LacpRxMachinePortDisabled)
	rules.AddRule(LacpRxmStateDefaulted, LacpRxmEventNotPortEnabledAndNotPortMoved, rxm.LacpRxMachinePortDisabled)
	rules.AddRule(LacpRxmStateCurrent, LacpRxmEventNotPortEnabledAndNotPortMoved, rxm.LacpRxMachinePortDisabled)
	// PORT MOVED
	rules.AddRule(LacpRxmStatePortDisabled, LacpRxmEventPortMoved, rxm.LacpRxMachineInitialize)
	// PORT ENABLED && LACP ENABLED
	rules.AddRule(LacpRxmStatePortDisabled, LacpRxmEventPortEnabledAndLacpEnabled, rxm.LacpRxMachineExpired)
	// PORT ENABLED && LACP DISABLED
	rules.AddRule(LacpRxmStatePortDisabled, LacpRxmEventPortEnabledAndLacpDisabled, rxm.LacpRxMachineLacpDisabled)
	// CURRENT WHILE TIMER EXPIRED
	rules.AddRule(LacpRxmStateExpired, LacpRxmEventCurrentWhileTimerExpired, rxm.LacpRxMachineDefaulted)
	rules.AddRule(LacpRxmStateCurrent, LacpRxmEventCurrentWhileTimerExpired, rxm.LacpRxMachineExpired)
	// LACP ENABLED
	rules.AddRule(LacpRxmStateLacpDisabled, LacpRxmEventLacpEnabled, rxm.LacpRxMachinePortDisabled)
	// PKT RX
	rules.AddRule(LacpRxmStateExpired, LacpRxmEventLacpPktRx, rxm.LacpRxMachineCurrent)
	rules.AddRule(LacpRxmStateDefaulted, LacpRxmEventLacpPktRx, rxm.LacpRxMachineCurrent)
	rules.AddRule(LacpRxmStateCurrent, LacpRxmEventLacpPktRx, rxm.LacpRxMachineCurrent)

	// Create a new FSM and apply the rules
	rxm.Apply(&rules)

	return rxm
}

// LacpRxMachineMain:  802.1ax-2014 Table 6-18
// Creation of Rx State Machine state transitions and callbacks
// and create go routine to pend on events
func (p *LaAggPort) LacpRxMachineMain() {

	// initialize the port
	p.begin = true

	// Build the state machine for Lacp Receive Machine according to
	// 802.1ax Section 6.4.12 Receive Machine
	rxm := LacpRxMachineFSMBuild(p)

	// set the inital state
	rxm.Machine.Start(rxm.PrevState())

	// lets create a go routing which will wait for the specific events
	// that the RxMachine should handle.
	go func(m *LacpRxMachine) {
		rxm.LacpRxmLog("PTXM: Machine Start")
		select {
		case <-m.RxmKillSignalEvent:
			rxm.LacpRxmLog("RXM: Machine End")
			return

		case event := <-m.RxmEvents:
			m.Machine.ProcessEvent(event, nil)
			/* special case */
			if m.Machine.Curr.CurrentState() == LacpRxmStateInitialize {
				m.Machine.ProcessEvent(LacpRxmEventUnconditionalFallthrough, nil)
			}

		case pkt := <-rxm.RxmPktRxEvent:
			fmt.Println(pkt)
			// If you rx a packet must be in one
			// of 3 states
			// Expired/Defaulted/Current. each
			// state will transition to current
			// all other states should be ignored.

			m.Machine.ProcessEvent(LacpRxmEventLacpPktRx, pkt)
		case ena := <-m.RxmLogEnableEvent:
			m.logEna = ena

		}
	}(rxm)
}

// handleRxFrame:
// TBD: First entry point of the raw ethernet frame
//func handleRxFrame(port int, pdu []bytes) {

// TODO
//	lacp := LacpPdu()
//	err := binary.Read(pdu, binary.BigEndian, &lacp)
//	if err != nil {
//		panic(err)
//	}
//}

// recordPDU: 802.1ax Section 6.4.9
//
// Record actor informatio from the packet
// Clear Defaulted Actor Operational state
// Determine Partner Operational Sync state
func (rxm *LacpRxMachine) recordPDU(lacpPduInfo *LacpPdu) {

	p := rxm.p

	// Record Actor info from packet - store in parter operational
	// Port Number, Port Priority, System, System Priority
	// Key, state variables
	LacpCopyLacpPortInfo(&lacpPduInfo.actor.info, &p.partnerOper)

	// Set Actor Oper port state Defaulted to FALSE
	LacpStateClear(p.actorOper.state, LacpStateDefaultedBit)

	// Set Partner Oper port state Sync state to
	// TRUE if the (1) or (2) is true:
	//
	// 1) Rx pdu: (Partner Port, Partner Port Priority, Partner
	// System, Partner System Priority, Partner Key,
	// Partner state Aggregation) vs 	cooresponding Operational
	// parameters of the Actor and Actor state Sync is TRUE and (3)
	//
	// 2) Rx pdu: Value of Actor state aggregation is FALSE
	// (indicates individual link) and Actor state sync is TRUE
	// and (3)
	//
	// 3) Rx pdu: Actor state LACP_Activity is TRUE
	// or both Actor Oper Port state LACP_Activity and PDU Partner
	// Partner state LACP_Activity is TRUE

	// (1)
	if ((LacpLacpPortInfoIsEqual(&lacpPduInfo.partner.info, &p.actorOper, LacpStateAggregationBit) &&
		LacpStateIsSet(lacpPduInfo.actor.info.state, LacpStateSyncBit)) ||
		//(2)
		(!LacpStateIsSet(lacpPduInfo.actor.info.state, LacpStateAggregationBit) &&
			LacpStateIsSet(lacpPduInfo.actor.info.state, LacpStateSyncBit))) &&
		// (3)
		(LacpStateIsSet(lacpPduInfo.actor.info.state, LacpStateActivityBit) ||
			(LacpStateIsSet(p.actorOper.state, LacpStateActivityBit) &&
				LacpStateIsSet(lacpPduInfo.partner.info.state, LacpStateActivityBit))) {

		LacpStateSet(p.partnerOper.state, LacpStateSyncBit)
	} else {
		LacpStateClear(p.partnerOper.state, LacpStateSyncBit)
	}

	// Optional to validate length of the following:
	// actor, partner, collector
}

// recordDefault: 802.1ax Section 6.4.9
//
// records the default parameter values for the
// partner carried in the partner admin parameters
// (Partner Admin Port Number, Partner Admin Port Priority,
//  Partner Admin System, Partner Admin System Priority,
// Partner Admin Key, and Partner Admin Port state) as the
// current Partner operational parameter values.  Sets Actor
// Oper Port state Default to TRUE and Partner Oper Port State
// Sync to TRUE
func (rxm *LacpRxMachine) recordDefault() {

	p := rxm.p

	LacpCopyLacpPortInfo(&p.partnerAdmin, &p.partnerOper)
	LacpStateSet(p.actorOper.state, LacpStateDefaultedBit)
	LacpStateSet(p.partnerOper.state, LacpStateSyncBit)
}

// updateSelected:  802.1ax Section 6.4.9
// Sets the value of the Selected variable based on the following:
//
// Rx pdu: (Actor: Port, Priority, System, System Priority, Key
// and State Aggregation) vs (Partner Oper: Port, Priority,
// System, System Priority, Key, State Aggregation).  If values
// have changed then Selected is set to UNSELECTED, othewise
// SELECTED
func (rxm *LacpRxMachine) updateSelected(lacpPduInfo *LacpPdu) {

	p := rxm.p

	if !LacpLacpPortInfoIsEqual(&lacpPduInfo.actor.info, &p.partnerOper, LacpStateAggregationBit) {

		p.aggSelected = LacpAggUnSelected
		// lets trigger the event only if mux is not in waiting state as
		// the wait while timer expiration will trigger the unselected event
		if p.muxMachineFsm.Machine.Curr.CurrentState() == LacpMuxmStateWaiting ||
			p.muxMachineFsm.Machine.Curr.CurrentState() == LacpMuxmStateWaiting {

		}

	}
}

// updateDefaultedSelected: 802.1ax Section 6.4.9
//
// Update the value of the Selected variable comparing
// the Partner admin info based with the partner
// operational info
// (port num, port priority, system, system priority,
//  key, stat.Aggregation)
func (rxm *LacpRxMachine) updateDefaultSelected() {

	p := rxm.p

	if !LacpLacpPortInfoIsEqual(&p.partnerAdmin, &p.partnerOper, LacpStateAggregationBit) {

		p.aggSelected = LacpAggUnSelected
	}
}

// updateNTT: 802.1ax Section 6.4.9
//
// Compare that the newly received PDU partner
// info agrees with the local port oper state.
// If it does not agree then set the NTT flag
// such that the Tx machine generates LACPDU
func (rxm *LacpRxMachine) updateNTT(lacpPduInfo *LacpPdu) bool {

	p := rxm.p

	const nttStateCompare uint8 = (LacpStateActivityBit | LacpStateTimeoutBit |
		LacpStateAggregationBit | LacpStateSyncBit)

	if !LacpLacpPortInfoIsEqual(&lacpPduInfo.partner.info, &p.partnerOper, nttStateCompare) {

		return true
	}
	return false
}

func (rxm *LacpRxMachine) recordVersionNumber(lacpPduInfo *LacpPdu) {

	p := rxm.p

	p.partnerVersion = lacpPduInfo.version
}
