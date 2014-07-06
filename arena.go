// Copyright 2014 Team 254. All Rights Reserved.
// Author: pat@patfairbank.com (Patrick Fairbank)
//
// Functions for controlling the arena and match play.

package main

import (
	"fmt"
	"time"
)

// Loop and match timing constants.
const (
	arenaLoopPeriodMs  = 10
	dsPacketPeriodMs   = 250
	autoDurationSec    = 10
	pauseDurationSec   = 1
	teleopDurationSec  = 140
	endgameTimeLeftSec = 30
)

// Progression of match states.
const (
	PRE_MATCH = iota
	START_MATCH
	AUTO_PERIOD
	PAUSE_PERIOD
	TELEOP_PERIOD
	ENDGAME_PERIOD
	POST_MATCH
)

type AllianceStation struct {
	DsConn        *DriverStationConnection
	EmergencyStop bool
	Bypass        bool
	team          *Team
}

type Arena struct {
	AllianceStations map[string]*AllianceStation
	MatchState       int
	CanStartMatch    bool
	currentMatch     *Match
	matchStartTime   time.Time
	lastDsPacketTime time.Time
}

var mainArena Arena // Named thusly to avoid polluting the global namespace with something more generic.

// Sets the arena to its initial state.
func (arena *Arena) Setup() {
	arena.AllianceStations = make(map[string]*AllianceStation)
	arena.AllianceStations["R1"] = new(AllianceStation)
	arena.AllianceStations["R2"] = new(AllianceStation)
	arena.AllianceStations["R3"] = new(AllianceStation)
	arena.AllianceStations["B1"] = new(AllianceStation)
	arena.AllianceStations["B2"] = new(AllianceStation)
	arena.AllianceStations["B3"] = new(AllianceStation)

	// Load empty match as current.
	arena.MatchState = PRE_MATCH
	arena.LoadTestMatch()
}

// Loads a team into an alliance station, cleaning up the previous team there if there is one.
func (arena *Arena) AssignTeam(teamId int, station string) error {
	// Reject invalid station values.
	if _, ok := arena.AllianceStations[station]; !ok {
		return fmt.Errorf("Invalid alliance station '%s'.", station)
	}

	// Do nothing if the station is already assigned to the requested team.
	dsConn := arena.AllianceStations[station].DsConn
	if dsConn != nil && dsConn.TeamId == teamId {
		return nil
	}
	if dsConn != nil {
		err := dsConn.Close()
		if err != nil {
			return err
		}
		arena.AllianceStations[station].team = nil
		arena.AllianceStations[station].DsConn = nil
	}

	// Leave the station empty if the team number is zero.
	if teamId == 0 {
		return nil
	}

	// Load the team model. Raise an error if a team doesn't exist.
	team, err := db.GetTeamById(teamId)
	if err != nil {
		return err
	}
	if team == nil {
		return fmt.Errorf("Invalid team number '%d'.", teamId)
	}

	arena.AllianceStations[station].team = team
	arena.AllianceStations[station].DsConn, err = NewDriverStationConnection(team.Id, station)
	if err != nil {
		return err
	}
	return nil
}

// Sets up the arena for the given match.
func (arena *Arena) LoadMatch(match *Match) error {
	if arena.MatchState != PRE_MATCH {
		return fmt.Errorf("Cannot load match while there is a match still in progress or with results pending.")
	}

	arena.currentMatch = match
	err := arena.AssignTeam(match.Red1, "R1")
	if err != nil {
		return err
	}
	err = arena.AssignTeam(match.Red2, "R2")
	if err != nil {
		return err
	}
	err = arena.AssignTeam(match.Red3, "R3")
	if err != nil {
		return err
	}
	err = arena.AssignTeam(match.Blue1, "B1")
	if err != nil {
		return err
	}
	err = arena.AssignTeam(match.Blue2, "B2")
	if err != nil {
		return err
	}
	err = arena.AssignTeam(match.Blue3, "B3")
	if err != nil {
		return err
	}
	return nil
}

// Sets a new test match as the current match.
func (arena *Arena) LoadTestMatch() error {
	return arena.LoadMatch(&Match{Type: "test"})
}

// Loads the first unplayed match of the current match type.
func (arena *Arena) LoadNextMatch() error {
	if arena.currentMatch.Type == "test" {
		return arena.LoadTestMatch()
	}

	matches, err := db.GetMatchesByType(arena.currentMatch.Type)
	if err != nil {
		return err
	}
	for _, match := range matches {
		if match.Status != "complete" {
			err = arena.LoadMatch(&match)
			if err != nil {
				return err
			}
			break
		}
	}
	return nil
}

// Assigns the given team to the given station, also substituting it into the match record.
func (arena *Arena) SubstituteTeam(teamId int, station string) error {
	if arena.currentMatch.Type != "test" && arena.currentMatch.Type != "practice" {
		return fmt.Errorf("Can only substitute teams for test and practice matches.")
	}
	err := arena.AssignTeam(teamId, station)
	if err != nil {
		return err
	}
	switch station {
	case "R1":
		arena.currentMatch.Red1 = teamId
	case "R2":
		arena.currentMatch.Red2 = teamId
	case "R3":
		arena.currentMatch.Red3 = teamId
	case "B1":
		arena.currentMatch.Blue1 = teamId
	case "B2":
		arena.currentMatch.Blue2 = teamId
	case "B3":
		arena.currentMatch.Blue3 = teamId
	}
	return nil
}

// Returns nil if the match can be started, and an error otherwise.
func (arena *Arena) CheckCanStartMatch() error {
	if arena.MatchState != PRE_MATCH {
		return fmt.Errorf("Cannot start match while there is a match still in progress or with results pending.")
	}
	for _, allianceStation := range arena.AllianceStations {
		if allianceStation.EmergencyStop {
			return fmt.Errorf("Cannot start match while an emergency stop is active.")
		}
		if !allianceStation.Bypass {
			if allianceStation.DsConn == nil || !allianceStation.DsConn.DriverStationStatus.RobotLinked {
				return fmt.Errorf("Cannot start match until all robots are connected or bypassed.")
			}
		}
	}
	return nil
}

// Starts the match if all conditions are met.
func (arena *Arena) StartMatch() error {
	err := arena.CheckCanStartMatch()
	if err == nil {
		arena.MatchState = START_MATCH
	}
	return err
}

// Kills the current match if it is underway.
func (arena *Arena) AbortMatch() error {
	if arena.MatchState == PRE_MATCH || arena.MatchState == POST_MATCH {
		return fmt.Errorf("Cannot abort match when it is not in progress.")
	}
	arena.MatchState = POST_MATCH
	return nil
}

// Clears out the match and resets the arena state unless there is a match underway.
func (arena *Arena) ResetMatch() error {
	if arena.MatchState != POST_MATCH && arena.MatchState != PRE_MATCH {
		return fmt.Errorf("Cannot reset match while it is in progress.")
	}
	arena.MatchState = PRE_MATCH
	arena.AllianceStations["R1"].Bypass = false
	arena.AllianceStations["R2"].Bypass = false
	arena.AllianceStations["R3"].Bypass = false
	arena.AllianceStations["B1"].Bypass = false
	arena.AllianceStations["B2"].Bypass = false
	arena.AllianceStations["B3"].Bypass = false
	return nil
}

// Performs a single iteration of checking inputs and timers and setting outputs accordingly to control the
// flow of a match.
func (arena *Arena) Update() {
	arena.CanStartMatch = arena.CheckCanStartMatch() == nil

	// Decide what state the robots need to be in, depending on where we are in the match.
	auto := false
	enabled := false
	sendDsPacket := false
	matchTimeSec := arena.MatchTimeSec()
	switch arena.MatchState {
	case PRE_MATCH:
		auto = true
		enabled = false
	case START_MATCH:
		arena.MatchState = AUTO_PERIOD
		arena.matchStartTime = time.Now()
		auto = true
		enabled = true
		sendDsPacket = true
	case AUTO_PERIOD:
		auto = true
		enabled = true
		if matchTimeSec >= autoDurationSec {
			arena.MatchState = PAUSE_PERIOD
			auto = false
			enabled = false
			sendDsPacket = true
		}
	case PAUSE_PERIOD:
		auto = false
		enabled = false
		if matchTimeSec >= autoDurationSec+pauseDurationSec {
			arena.MatchState = TELEOP_PERIOD
			auto = false
			enabled = true
			sendDsPacket = true
		}
	case TELEOP_PERIOD:
		auto = false
		enabled = true
		if matchTimeSec >= autoDurationSec+pauseDurationSec+teleopDurationSec-endgameTimeLeftSec {
			arena.MatchState = ENDGAME_PERIOD
			sendDsPacket = false
		}
	case ENDGAME_PERIOD:
		auto = false
		enabled = true
		if matchTimeSec >= autoDurationSec+pauseDurationSec+teleopDurationSec {
			arena.MatchState = POST_MATCH
			auto = false
			enabled = false
			sendDsPacket = true
		}
	}

	// Send a packet if at a period transition point or if it's been long enough since the last one.
	if sendDsPacket || time.Since(arena.lastDsPacketTime).Seconds()*1000 >= dsPacketPeriodMs {
		arena.sendDsPacket(auto, enabled)
	}
}

// Loops indefinitely to track and update the arena components.
func (arena *Arena) Run() {
	for {
		arena.Update()
		time.Sleep(time.Millisecond * arenaLoopPeriodMs)
	}
}

func (arena *Arena) sendDsPacket(auto bool, enabled bool) {
	for _, allianceStation := range arena.AllianceStations {
		if allianceStation.DsConn != nil {
			allianceStation.DsConn.Auto = auto
			allianceStation.DsConn.Enabled = enabled && !allianceStation.EmergencyStop && !allianceStation.Bypass
			err := allianceStation.DsConn.Update()
			if err != nil {
				// TODO(pat): Handle errors.
			}
		}
	}
	arena.lastDsPacketTime = time.Now()
}

// Returns the fractional number of seconds since the start of the match.
func (arena *Arena) MatchTimeSec() float64 {
	if arena.MatchState == PRE_MATCH || arena.MatchState == POST_MATCH {
		return 0
	} else {
		return time.Since(arena.matchStartTime).Seconds()
	}
}
