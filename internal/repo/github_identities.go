package repo

import (
	"agentflow/internal/domain"
	"errors"
	"strings"
)

// GitHub sign-in is keyed by GitHub's numeric account ID, never by login:
// GitHub logins can be renamed and reused by someone else.
type githubIdentityRow struct {
	GitHubID int64  `gorm:"column:github_id;primaryKey;autoIncrement:false"`
	UserID   int64  `gorm:"column:user_id;not null;uniqueIndex:uq_user_github_identities_user"`
	Login    string `gorm:"column:login;size:64"`
}

func (githubIdentityRow) TableName() string { return "user_github_identities" }

func (r *UsersRepo) UserByGitHubID(githubID int64) (domain.User, bool) {
	var row githubIdentityRow
	if githubID <= 0 || r.DB.Where("github_id = ?", githubID).Limit(1).Find(&row).RowsAffected == 0 {
		return domain.User{}, false
	}
	return r.User(row.UserID)
}

// CreateGitHubAccount registers a user for a GitHub account that has no
// LoopForge user yet. It takes the first free login among candidates and
// never takes over an existing user row.
func (r *UsersRepo) CreateGitHubAccount(githubID int64, githubLogin, displayName string, candidates []string) (domain.User, error) {
	if githubID <= 0 {
		return domain.User{}, errors.New("github account id is required")
	}
	if u, ok := r.UserByGitHubID(githubID); ok {
		return u, nil
	}
	login := ""
	for _, candidate := range candidates {
		if _, taken := r.UserByLoginName(candidate); !taken {
			login = candidate
			break
		}
	}
	if login == "" {
		return domain.User{}, ErrLoginTaken
	}
	if strings.TrimSpace(displayName) == "" {
		displayName = githubLogin
	}
	if err := r.PutUser(domain.User{LoginName: login, ChineseName: displayName}); err != nil {
		return domain.User{}, err
	}
	u, ok := r.UserByLoginName(login)
	if !ok {
		return domain.User{}, errors.New("could not read account")
	}
	if err := r.DB.Create(&githubIdentityRow{GitHubID: githubID, UserID: u.ID, Login: githubLogin}).Error; err != nil {
		return domain.User{}, err
	}
	return u, nil
}

// TouchGitHubLogin records the current GitHub login for an existing link.
func (r *UsersRepo) TouchGitHubLogin(githubID int64, githubLogin string) error {
	return r.DB.Model(&githubIdentityRow{}).Where("github_id = ?", githubID).Update("login", githubLogin).Error
}
