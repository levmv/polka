package db

import "fmt"

type CleanupCounts struct {
	MissingCover  int
	UnknownAuthor int
	NoTags        int
	NoDescription int
}

func GetCleanupCounts(queryer Queryer, scope VisibilityScope) (CleanupCounts, error) {
	var counts CleanupCounts
	var err error
	if counts.MissingCover, err = countBooksByCondition(queryer, scope, noCoverCondition); err != nil {
		return counts, err
	}
	if counts.UnknownAuthor, err = countBooksByCondition(queryer, scope, noAuthorCondition); err != nil {
		return counts, err
	}
	if counts.NoTags, err = countBooksByCondition(queryer, scope, noTagsCondition); err != nil {
		return counts, err
	}
	if counts.NoDescription, err = countBooksByCondition(queryer, scope, noDescriptionCondition); err != nil {
		return counts, err
	}
	return counts, nil
}

func countBooksByCondition(queryer Queryer, scope VisibilityScope, condition string) (int, error) {
	where, args := scope.AppendWorkWhere("w.deleted_at IS NULL AND ("+condition+")", "w.id")
	var count int
	if err := queryer.QueryRow(`SELECT COUNT(*) FROM works w WHERE `+where, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("count cleanup books: %w", err)
	}
	return count, nil
}
