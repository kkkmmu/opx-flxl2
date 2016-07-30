//
//Copyright [2016] [SnapRoute Inc]
//
//Licensed under the Apache License, Version 2.0 (the "License");
//you may not use this file except in compliance with the License.
//You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
//	 Unless required by applicable law or agreed to in writing, software
//	 distributed under the License is distributed on an "AS IS" BASIS,
//	 WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//	 See the License for the specific language governing permissions and
//	 limitations under the License.
//
// _______  __       __________   ___      _______.____    __    ____  __  .___________.  ______  __    __
// |   ____||  |     |   ____\  \ /  /     /       |\   \  /  \  /   / |  | |           | /      ||  |  |  |
// |  |__   |  |     |  |__   \  V  /     |   (----` \   \/    \/   /  |  | `---|  |----`|  ,----'|  |__|  |
// |   __|  |  |     |   __|   >   <       \   \      \            /   |  |     |  |     |  |     |   __   |
// |  |     |  `----.|  |____ /  .  \  .----)   |      \    /\    /    |  |     |  |     |  `----.|  |  |  |
// |__|     |_______||_______/__/ \__\ |_______/        \__/  \__/     |__|     |__|      \______||__|  |__|
//
// 802.1ax-2014 Section 9.4.15 DRCPDU Periodic Transmission machine
// rxmachine.go
package drcp

import (
	"github.com/google/gopacket/layers"
	"l2/lacp/protocol/utils"
	"sort"
	"time"
)

const GMachineModuleStr = "DRNI Gateway Machine"

// drxm States
const (
	GmStateNone = iota + 1
	GmStateDRNIGatewayInitialize
	GmStateDRNIGatewayUpdate
	GmStatePsGatewayUpdate
)

var GmStateStrMap map[fsm.State]string

func GMachineStrStateMapCreate() {
	GmStateStrMap = make(map[fsm.State]string)
	GmStateStrMap[GmStateNone] = "None"
	GmStateStrMap[GmStateDRNIGatewayInitialize] = "DRNI Gateway Initialize"
	GmStateStrMap[GmStateDRNIGatewayUpdate] = "DRNI Gateway Update"
	GmStateStrMap[GmStatePsGatewayUpdate] = "PS Gateway Update"
}

// rxm events
const (
	GmEventBegin = iota + 1
	GmEventGatewayConversationUpdate
	GmEventNotIppAllGatewayUpdate
)

// GMachine holds FSM and current State
// and event channels for State transitions
type GMachine struct {
	// for debugging
	PreviousState fsm.State

	Machine *fsm.Machine

	dr *DistributedRelay

	// machine specific events
	GmEvents chan utils.MachineEvent
}

func (gm *GMachine) PrevState() fsm.State { return gm.PreviousState }

// PrevStateSet will set the previous State
func (gm *GMachine) PrevStateSet(s fsm.State) { gm.PreviousState = s }

// Stop should clean up all resources
func (gm *GMachine) Stop() {
	close(gm.GmEvents)
}

// NewDrcpGMachine will create a new instance of the GMachine
func NewDrcpGMachine(port *DRCPIpp, conversationIdtype int) *GMachine {
	gm := &GMachine{
		dr:            dr,
		PreviousState: GmStateNone,
		GmEvents:      make(chan MachineEvent, 10),
	}

	dr.GMachineFsm[conversationIdtype] = gm

	return gm
}

// A helpful function that lets us apply arbitrary rulesets to this
// instances State machine without reallocating the machine.
func (gm *GMachine) Apply(r *fsm.Ruleset) *fsm.Machine {
	if gm.Machine == nil {
		gm.Machine = &fsm.Machine{}
	}

	// Assign the ruleset to be used for this machine
	gm.Machine.Rules = r
	gm.Machine.Curr = &utils.StateEvent{
		StrStateMap: GmStateStrMap,
		LogEna:      true,
		Logger:      gm.DrcpGmLog,
		Owner:       GMachineModuleStr,
	}

	return gm.Machine
}

// DrcpGMachineDRNIGatewayInitialize function to be called after
// State transition to DRNI_GATEWAY_INITIALIZE
func (gm *GMachine) DrcpGMachineDRNIGatewayInitialize(m fsm.Machine, data interface{}) fsm.State {
	dr := gm.dr
	gm.initializeDRNIGatewayConversation()
	dr.GatewayConversationUpdate = false

	return GmStateDRNIGatewayInitialize
}

// DrcpGMachineDRNIGatewayUpdate function to be called after
// State transition to DRNI_GATEWAY_UPDATE
func (gm *GMachine) DrcpGMachineDRNIGatewayUpdate(m fsm.Machine, data interface{}) fsm.State {
	dr := gm.dr

	dr.GatewayConversationUpdate = false
	gm.updatePortalState()
	gm.setIPPGatewayUpdate()
	gm.setGatewayConversation()

	// next State
	return GmStateDRNIGatewayUpdate
}

// DrcpGMachinePSGatewayUpdate function to be called after
// State transition to PS_GATEWAY_UPDATE
func (gm *GMachine) DrcpGMachinePSGatewayUpdate(m fsm.Machine, data interface{}) fsm.State {
	gm.updatePortalSystemGatewayConversation()

	// next State
	return GmStateDRNIGatewayUpdate
}

func DrcpGMachineFSMBuild(p *DRCPIpp) *GMachine {

	GMachineStrStateMapCreate()

	rules := fsm.Ruleset{}

	// Instantiate a new GMachine
	// Initial State will be a psuedo State known as "begin" so that
	// we can transition to the initalize State
	gm := NewDrcpGMachine(p)

	//BEGIN -> DRNI GATEWAY INITIALIZE
	rules.AddRule(GmStateNone, GmEventBegin, gm.DrcpGMachineDRNIGatewayInitialize)
	rules.AddRule(GmStateDRNIGatewayUpdate, GmEventBegin, gm.DrcpGMachineDRNIGatewayInitialize)
	rules.AddRule(GmStatePsGatewayUpdate, GmEventBegin, gm.DrcpGMachineDRNIGatewayInitialize)

	// GATEWAY CONVERSATION UPDATE  > DRNI GATEWAY UPDATE
	rules.AddRule(GmStateDRNIGatewayInitialize, GmEventGatewayConversationUpdate, gm.DrcpGMachineDRNIGatewayUpdate)
	rules.AddRule(GmStateDRNIGatewayUpdate, GmEventGatewayConversationUpdate, gm.DrcpGMachineDRNIGatewayUpdate)
	rules.AddRule(GmStatePsGatewayUpdate, GmEventGatewayConversationUpdate, gm.DrcpGMachineDRNIGatewayUpdate)

	// NOT IPP ALL GATEWAY UPDATE  > PS GATEWAY UPDATE
	rules.AddRule(GmStateDRNIGatewayUpdate, GmEventNotIppAllGatewayUpdate, gm.DrcpGMachinePSGatewayUpdate)

	// Create a new FSM and apply the rules
	rxm.Apply(&rules)

	return rxm
}

// DrcpGMachineMain:  802.1ax-2014 Figure 9-26
// Creation of DRNI Gateway State Machine state transitions and callbacks
// and create go routine to pend on events
func (p *DRCPIpp) DrcpGMachineMain() {

	// Build the State machine for  DRNI Gateway Machine according to
	// 802.1ax-2014 Section 9.4.17 DRNI Gateway and Aggregator machines
	gm := DrcpGMachineFSMBuild(p)
	p.wg.Add(1)

	// set the inital State
	gm.Machine.Start(gm.PrevState())

	// lets create a go routing which will wait for the specific events
	// that the GMachine should handle.
	go func(m *GMachine) {
		m.DrcpGmLog("Machine Start")
		defer m.p.wg.Done()
		for {
			select {
			case event, ok := <-m.GmEvents:
				if ok {
					rv := m.Machine.ProcessEvent(event.src, event.e, nil)
					p := m.p
					// post state processing
					if rv == nil &&
						dr.GatewayConversationUpdate {
						rv = m.Machine.ProcessEvent(GMachineModuleStr, GmEventGatewayConversationUpdate, nil)
					}
					if rv == nil &&
						m.Machine.Curr.CurrentState() == GmStateDRNIGatewayUpdate &&
						!m.IppAllGatewayUpdateCheck() {
						rv = m.Machine.ProcessEvent(GMachineModuleStr, GmEventNotIppAllGatewayUpdate, nil)
					}

					if rv != nil {
						m.DrcpGmLog(strings.Join([]string{error.Error(rv), event.src, GmStateStrMap[m.Machine.Curr.CurrentState()], strconv.Itoa(int(event.e))}, ":"))
					}
				}

				// respond to caller if necessary so that we don't have a deadlock
				if event.ResponseChan != nil {
					utils.SendResponse(GMachineModuleStr, event.ResponseChan)
				}
				if !ok {
					m.DrcpGmLog("Machine End")
					return
				}
			}
		}
	}(gm)
}

// IppAllGatewayUpdateCheck Check is made in order to try and perform the following logic
// needed for IppAllGatewayUpdate; This variable is the logical OR of the IppGatewayUpdate
// variables for all IPPs in this Portal System.
func (gm *GMachine) IppAllGatewayUpdateCheck() bool {
	dr := gm.dr
	for _, ippid := range dr.DrniIntraPortalLinkList {
		for _, ipp := range DRCPIppDBList {
			if ipp.Id == ippid {
				if ipp.IppGatewayUpdate {
					dr.IppAllGatewayUpdate = true
				}
			}
		}
	}
	return dr.IppAllGatewayUpdate
}

// initializeDRNIGatewayConversation This function sets the Drni_Portal_System_Gateway_Conversation to a sequence of zeros,
// indexed by Gateway Conversation ID
func (gm *GMachine) initializeDRNIGatewayConversation() {
	dr := gm.dr

	for i := 0; i < MAX_GATEWAY_CONVERSATION; i++ {
		dr.DrniPortalSystemGatewayConversation[i] = false
	}
}

// updatePortalState This function updates the Drni_Portal_System_State[] as follows
func (gm *GMachine) updatePortalState() {
	// TODO need to understand the logic better
}

// setIPPGatewayUpdate This function sets the IppGatewayUpdate on every IPP on this
// Portal System to TRUE
func (gm *GMachine) setIPPGatewayUpdate() {
	dr := gm.dr
	for _, ippid := range dr.DrniIntraPortalLinkList {
		for _, ipp := range DRCPIppDBList {
			if ipp.Id == ippid {
				ipp.IppGatewayUpdate = true
			}
		}
	}
}

// setGatewayConversation This function sets Drni_Gateway_Conversation to the
// values computed from aDrniConvAdminGateway[] and the current
// Drni_Portal_System_State[] as follows
func (gm *GMachine) setGatewayConversation() {
	dr := gm.dr
	for i := 0; i < 4096; i++ {
		// TODO
		// For every indexed Gateway Conversation ID, a Portal System Number is identified by
		// choosing the highest priority Portal System Number in the list of Portal System Numbers
		// provided by aDrniConvAdminGateway[] when only the Portal Systems having that Gateway
		// Conversation ID enabled in the Gateway Vectors of the Drni_Portal_System_State[] variable,
		// are included.
	}
}

// updatePortalSystemGatewayConversation This function sets Drni_Portal_System_Gateway_Conversation as follows
func (gm *GMachine) updatePortalSystemGatewayConversation() {
	/* TODO need to understand this better to apply the port
	dr := gm.dr
	if p.DifferGatewayDigest &&
		!dr.DrniThreeSystemPortal {
		for i := 0; i < 512; i++ {
			dr.DrniPortalSystemGatewayConversation[i] = p.DrniNeighborGatewayConversation[i]>>3&0x1 == 1
			dr.DrniPortalSystemGatewayConversation[i+1] = p.DrniNeighborGatewayConversation[i]>>2&0x1 == 1
			dr.DrniPortalSystemGatewayConversation[i+2] = p.DrniNeighborGatewayConversation[i]>>1&0x1 == 1
			dr.DrniPortalSystemGatewayConversation[i+3] = p.DrniNeighborGatewayConversation[i]>>0&0x1 == 1
		}
	} else {
		for i := 0; i < 4096; i++ {
			// TODO what is the mapping of conversation id to system portal id
		}
	}
	*/
}
