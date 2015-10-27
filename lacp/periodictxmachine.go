// The Periodic Transmission Machine is described in the 802.1ax-2014 Section 6.4.13
package lacp

import (
	"time"
	"utils/fsm"
)

const PtxMachineModuleStr = "Periodic TX Machine"

const (
	LacpPtxmStateNone = iota + 1
	LacpPtxmStateNoPeriodic
	LacpPtxmStateFastPeriodic
	LacpPtxmStateSlowPeriodic
	LacpPtxmStatePeriodicTx
)

var PtxmStateStrMap map[fsm.State]string

func PtxMachineStrStateMapCreate() {
	PtxmStateStrMap = make(map[fsm.State]string)
	PtxmStateStrMap[LacpPtxmStateNone] = "LacpPtxmStateNone"
	PtxmStateStrMap[LacpPtxmStateNoPeriodic] = "LacpPtxmStateNoPeriodic"
	PtxmStateStrMap[LacpPtxmStateFastPeriodic] = "LacpPtxmStateFastPeriodic"
	PtxmStateStrMap[LacpPtxmStateSlowPeriodic] = "LacpPtxmStateSlowPeriodic"
	PtxmStateStrMap[LacpPtxmStatePeriodicTx] = "LacpPtxmStatePeriodicTx"
}

const (
	LacpPtxmEventBegin = iota + 1
	LacpPtxmEventLacpDisabled
	LacpPtxmEventNotPortEnabled
	LacpPtxmEventActorPartnerOperActivityPassiveMode
	LacpPtxmEventUnconditionalFallthrough
	LacpPtxmEventPartnerOperStateTimeoutLong
	LacpPtxmEventPeriodicTimerExpired
	LacpPtxmEventPartnerOperStateTimeoutShort
)

// LacpRxMachine holds FSM and current state
// and event channels for state transitions
type LacpPtxMachine struct {
	// for debugging
	PreviousState fsm.State

	Machine *fsm.Machine

	// state transition log
	log chan string

	// Reference to LaAggPort
	p *LaAggPort

	// current tx interval LONG/SHORT
	PeriodicTxTimerInterval time.Duration

	// timer
	periodicTxTimer *time.Timer

	// machine specific events
	PtxmEvents chan LacpMachineEvent
	// stop go routine
	PtxmKillSignalEvent chan bool
	// enable logging
	PtxmLogEnableEvent chan bool
}

func (ptxm *LacpPtxMachine) PrevState() fsm.State { return ptxm.PreviousState }

// PrevStateSet will set the previous state
func (ptxm *LacpPtxMachine) PrevStateSet(s fsm.State) { ptxm.PreviousState = s }

func (ptxm *LacpPtxMachine) Stop() {
	ptxm.PeriodicTimerStop()

	ptxm.PtxmKillSignalEvent <- true

	close(ptxm.PtxmEvents)
	close(ptxm.PtxmKillSignalEvent)
	close(ptxm.PtxmLogEnableEvent)
}

// NewLacpRxMachine will create a new instance of the LacpRxMachine
func NewLacpPtxMachine(port *LaAggPort) *LacpPtxMachine {
	ptxm := &LacpPtxMachine{
		p:                       port,
		log:                     port.LacpDebug.LacpLogChan,
		PreviousState:           LacpPtxmStateNone,
		PeriodicTxTimerInterval: LacpSlowPeriodicTime,
		PtxmEvents:              make(chan LacpMachineEvent),
		PtxmKillSignalEvent:     make(chan bool),
		PtxmLogEnableEvent:      make(chan bool)}

	port.PtxMachineFsm = ptxm

	// start then stop
	ptxm.PeriodicTimerStart()
	ptxm.PeriodicTimerStop()

	return ptxm
}

// A helpful function that lets us apply arbitrary rulesets to this
// instances state machine without reallocating the machine.
func (ptxm *LacpPtxMachine) Apply(r *fsm.Ruleset) *fsm.Machine {
	if ptxm.Machine == nil {
		ptxm.Machine = &fsm.Machine{}
	}

	// Assign the ruleset to be used for this machine
	ptxm.Machine.Rules = r
	ptxm.Machine.Curr = &LacpStateEvent{
		strStateMap: PtxmStateStrMap,
		logEna:      ptxm.p.logEna,
		logger:      ptxm.LacpPtxmLog,
		owner:       PtxMachineModuleStr,
	}

	return ptxm.Machine
}

// LacpPtxMachineNoPeriodic stops the periodic transmission of packets
func (ptxm *LacpPtxMachine) LacpPtxMachineNoPeriodic(m fsm.Machine, data interface{}) fsm.State {
	ptxm.PeriodicTimerStop()
	return LacpPtxmStateNoPeriodic
}

// LacpPtxMachineFastPeriodic sets the periodic transmission time to fast
// and starts the timer
func (ptxm *LacpPtxMachine) LacpPtxMachineFastPeriodic(m fsm.Machine, data interface{}) fsm.State {
	ptxm.PeriodicTimerIntervalSet(LacpFastPeriodicTime)
	ptxm.PeriodicTimerStart()
	return LacpPtxmStateFastPeriodic
}

// LacpPtxMachineSlowPeriodic sets the periodic transmission time to slow
// and starts the timer
func (ptxm *LacpPtxMachine) LacpPtxMachineSlowPeriodic(m fsm.Machine, data interface{}) fsm.State {
	ptxm.PeriodicTimerIntervalSet(LacpSlowPeriodicTime)
	ptxm.PeriodicTimerStart()
	return LacpPtxmStateSlowPeriodic
}

// LacpPtxMachinePeriodicTx informs the tx machine that a packet should be transmitted by setting
// ntt = true
func (ptxm *LacpPtxMachine) LacpPtxMachinePeriodicTx(m fsm.Machine, data interface{}) fsm.State {
	// inform the tx machine that ntt should change to true which should transmit a
	// packet
	ptxm.p.TxMachineFsm.TxmEvents <- LacpMachineEvent{e: LacpTxmEventNtt}
	return LacpPtxmStatePeriodicTx
}

func LacpPtxMachineFSMBuild(p *LaAggPort) *LacpPtxMachine {

	rules := fsm.Ruleset{}

	PtxMachineStrStateMapCreate()

	// Instantiate a new LacpPtxMachine
	// Initial state will be a psuedo state known as "begin" so that
	// we can transition to the NO PERIODIC state
	ptxm := NewLacpPtxMachine(p)

	//BEGIN -> NO PERIODIC
	rules.AddRule(LacpPtxmStateNone, LacpPtxmEventBegin, ptxm.LacpPtxMachineNoPeriodic)
	rules.AddRule(LacpPtxmStateFastPeriodic, LacpPtxmEventBegin, ptxm.LacpPtxMachineNoPeriodic)
	rules.AddRule(LacpPtxmStateSlowPeriodic, LacpPtxmEventBegin, ptxm.LacpPtxMachineNoPeriodic)
	rules.AddRule(LacpPtxmStatePeriodicTx, LacpPtxmEventBegin, ptxm.LacpPtxMachineNoPeriodic)
	// LACP DISABLED -> NO PERIODIC
	rules.AddRule(LacpPtxmStateNone, LacpPtxmEventLacpDisabled, ptxm.LacpPtxMachineNoPeriodic)
	rules.AddRule(LacpPtxmStateFastPeriodic, LacpPtxmEventLacpDisabled, ptxm.LacpPtxMachineNoPeriodic)
	rules.AddRule(LacpPtxmStateSlowPeriodic, LacpPtxmEventLacpDisabled, ptxm.LacpPtxMachineNoPeriodic)
	rules.AddRule(LacpPtxmStatePeriodicTx, LacpPtxmEventLacpDisabled, ptxm.LacpPtxMachineNoPeriodic)
	// PORT DISABLED -> NO PERIODIC
	rules.AddRule(LacpPtxmStateNone, LacpPtxmEventNotPortEnabled, ptxm.LacpPtxMachineNoPeriodic)
	rules.AddRule(LacpPtxmStateFastPeriodic, LacpPtxmEventNotPortEnabled, ptxm.LacpPtxMachineNoPeriodic)
	rules.AddRule(LacpPtxmStateSlowPeriodic, LacpPtxmEventNotPortEnabled, ptxm.LacpPtxMachineNoPeriodic)
	rules.AddRule(LacpPtxmStatePeriodicTx, LacpPtxmEventNotPortEnabled, ptxm.LacpPtxMachineNoPeriodic)
	// ACTOR/PARTNER OPER STATE ACTIVITY MODE == PASSIVE -> NO PERIODIC
	rules.AddRule(LacpPtxmStateNone, LacpPtxmEventActorPartnerOperActivityPassiveMode, ptxm.LacpPtxMachineNoPeriodic)
	rules.AddRule(LacpPtxmStateFastPeriodic, LacpPtxmEventActorPartnerOperActivityPassiveMode, ptxm.LacpPtxMachineNoPeriodic)
	rules.AddRule(LacpPtxmStateSlowPeriodic, LacpPtxmEventActorPartnerOperActivityPassiveMode, ptxm.LacpPtxMachineNoPeriodic)
	rules.AddRule(LacpPtxmStatePeriodicTx, LacpPtxmEventActorPartnerOperActivityPassiveMode, ptxm.LacpPtxMachineNoPeriodic)
	// INTENTIONAL FALL THROUGH -> FAST PERIODIC
	rules.AddRule(LacpPtxmStateNoPeriodic, LacpPtxmEventUnconditionalFallthrough, ptxm.LacpPtxMachineFastPeriodic)
	// PARTNER OPER STAT LACP TIMEOUT == LONG -> SLOW PERIODIC
	rules.AddRule(LacpPtxmStateFastPeriodic, LacpPtxmEventPartnerOperStateTimeoutLong, ptxm.LacpPtxMachineSlowPeriodic)
	// PERIODIC TIMER EXPIRED -> PERIODIC TX
	rules.AddRule(LacpPtxmStateFastPeriodic, LacpPtxmEventPeriodicTimerExpired, ptxm.LacpPtxMachinePeriodicTx)
	// PARTNER OPER STAT LACP TIMEOUT == SHORT ->  PERIODIC TX
	rules.AddRule(LacpPtxmStateSlowPeriodic, LacpPtxmEventPartnerOperStateTimeoutShort, ptxm.LacpPtxMachinePeriodicTx)
	// PERIODIC TIMER EXPIRED -> PERIODIC TX
	rules.AddRule(LacpPtxmStateSlowPeriodic, LacpPtxmEventPeriodicTimerExpired, ptxm.LacpPtxMachinePeriodicTx)
	// PARTNER OPER STAT LACP TIMEOUT == SHORT ->  FAST PERIODIC
	rules.AddRule(LacpPtxmStatePeriodicTx, LacpPtxmEventPartnerOperStateTimeoutShort, ptxm.LacpPtxMachineFastPeriodic)
	// PARTNER OPER STAT LACP TIMEOUT == LONG -> SLOW PERIODIC
	rules.AddRule(LacpPtxmStatePeriodicTx, LacpPtxmEventPartnerOperStateTimeoutLong, ptxm.LacpPtxMachineSlowPeriodic)

	// Create a new FSM and apply the rules
	ptxm.Apply(&rules)

	return ptxm
}

// LacpRxMachineMain:  802.1ax-2014 Table 6-18
// Creation of Rx State Machine state transitions and callbacks
// and create go routine to pend on events
func (p *LaAggPort) LacpPtxMachineMain() {

	// Build the state machine for Lacp Receive Machine according to
	// 802.1ax Section 6.4.13 Periodic Transmission Machine
	ptxm := LacpPtxMachineFSMBuild(p)

	// set the inital state
	ptxm.Machine.Start(ptxm.PrevState())

	// lets create a go routing which will wait for the specific events
	// that the RxMachine should handle.
	go func(m *LacpPtxMachine) {
		m.LacpPtxmLog("PTXM: Machine Start")
		for {
			select {
			case <-m.PtxmKillSignalEvent:
				m.LacpPtxmLog("PTXM: Machine End")
				return

			case event := <-m.PtxmEvents:
				m.Machine.ProcessEvent(event.src, event.e, nil)
				/* special case */
				if m.LacpPtxIsNoPeriodicExitCondition() {
					m.Machine.ProcessEvent(PtxMachineModuleStr, LacpPtxmEventUnconditionalFallthrough, nil)
				}

				if event.e == LacpPtxmEventBegin && event.responseChan != nil {
					SendResponse("Periodic TX Machine", event.responseChan)
				}
			case ena := <-m.PtxmLogEnableEvent:
				m.Machine.Curr.EnableLogging(ena)
			}
		}
	}(ptxm)
}

// LacpPtxIsNoPeriodicExitCondition is meant to check if the UTC
// condition has been met when the state is NO PERIODIC
func (m *LacpPtxMachine) LacpPtxIsNoPeriodicExitCondition() bool {
	p := m.p
	return m.Machine.Curr.CurrentState() == LacpPtxmStateNoPeriodic &&
		p.lacpEnabled &&
		p.portEnabled &&
		(LacpModeGet(p.actorOper.state, p.lacpEnabled) == LacpModeActive ||
			LacpModeGet(p.partnerOper.state, p.lacpEnabled) == LacpModeActive)
}
