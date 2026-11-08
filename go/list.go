package relume

import (
	"context"
	"iter"
)

// List is a list endpoint, fetched a page at a time as you iterate:
//
//	list := client.ListLedger(ctx, relume.PageQuery{After: stored})
//	for entry, err := range list.All() {
//		if err != nil { … }
//		…
//	}
//	stored = list.NextCursor()
//
// Nothing is requested until iteration starts, and each iteration starts
// again from the After the list was made with. Range over Pages instead to
// handle (and checkpoint) a page at a time. Iterate a list once at a time:
// every iteration moves the same NextCursor.
type List[T any] struct {
	ctx   context.Context
	fetch func(ctx context.Context, after string) (Page[T], error)
	after string
	next  string
	// A refused argument, returned by the first iteration.
	err error
}

// NextCursor is where to resume: the NextCursor of the last page whose items
// have all been handed out, or the starting After ("" when there was none)
// before that. Once iteration ends, store it and pass it as After later to
// get only what is new (on the ledger; site cursors don't poll).
func (l *List[T]) NextCursor() string { return l.next }

// Pages yields every page in turn. The cursor advances when you ask for the
// next one.
func (l *List[T]) Pages() iter.Seq2[Page[T], error] {
	return func(yield func(Page[T], error) bool) {
		if l.err != nil {
			yield(Page[T]{}, l.err)
			return
		}
		after := l.after
		for {
			page, err := l.fetch(l.ctx, after)
			if err != nil {
				yield(Page[T]{}, err)
				return
			}
			if !yield(page, nil) {
				return
			}
			l.next = page.NextCursor
			if !page.HasMore {
				return
			}
			if page.NextCursor == after {
				// A page that says there's more but doesn't move would loop
				// forever.
				yield(Page[T]{}, &Error{Status: 200, Code: CodeUnexpectedResponse, Message: "The Relume API returned hasMore with the same cursor."})
				return
			}
			after = page.NextCursor
		}
	}
}

// All yields every item in turn, then stops at the first error.
func (l *List[T]) All() iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		for page, err := range l.Pages() {
			if err != nil {
				var zero T
				yield(zero, err)
				return
			}
			for _, item := range page.Data {
				if !yield(item, nil) {
					return
				}
			}
		}
	}
}

// Collect returns every item in one slice. Mind the size of what you're
// listing.
func (l *List[T]) Collect() ([]T, error) {
	var items []T
	for item, err := range l.All() {
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}
