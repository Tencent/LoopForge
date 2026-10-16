package domain

import "time"

// BusinessDomain is a managed reporting dimension, independent of project execution permissions.
type BusinessDomain struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Active      bool      `json:"active"`
	ManagerIDs  []int64   `json:"managerIds"`
	ViewerIDs   []int64   `json:"viewerIds"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type BusinessDomainAssignment struct {
	ID           int64     `json:"id"`
	ProjectID    string    `json:"projectId"`
	FromDomainID string    `json:"fromDomainId"`
	ToDomainID   string    `json:"toDomainId"`
	ActorID      int64     `json:"actorId"`
	Reason       string    `json:"reason"`
	CreatedAt    time.Time `json:"createdAt"`
}

type BusinessDomainProjectUsage struct {
	AnalyticsProjectUsage
	Name             string `json:"name"`
	BusinessDomainID string `json:"businessDomainId"`
}
type BusinessDomainUsage struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Active       bool    `json:"active"`
	ProjectCount int     `json:"projectCount"`
	Runs         int     `json:"runs"`
	Success      int     `json:"success"`
	Failed       int     `json:"failed"`
	Tokens       int64   `json:"tokens"`
	CostUSD      float64 `json:"costUsd"`
	CostKnown    bool    `json:"costKnown"`
	UniqueIssues int     `json:"uniqueIssues"`
	Delivered    int     `json:"delivered"`
}
type BusinessDomainReport struct {
	Attribution     string                       `json:"attribution"`
	GeneratedAt     time.Time                    `json:"generatedAt"`
	From            time.Time                    `json:"from"`
	To              time.Time                    `json:"to"`
	Summary         AnalyticsSummary             `json:"summary"`
	Delivery        AnalyticsDeliverySummary     `json:"delivery"`
	Trend           []AnalyticsTrendPoint        `json:"trend"`
	Projects        []BusinessDomainProjectUsage `json:"projects"`
	BusinessDomains []BusinessDomainUsage        `json:"businessDomains"`
}
