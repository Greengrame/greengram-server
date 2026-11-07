package media

import (
	"slices"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
)

// KindTodo is a checklist. All of it, ticks included, is TL on the message:
// there is nothing to share between copies, a forward starts its own.
const KindTodo = "todo"

// Telegram's todo_items_max, todo_title_length_max and todo_item_length_max.
const (
	TodoItemsMax = 30
	TodoTitleMax = 32
	TodoItemMax  = 64
)

var errTodoInvalid = tgerr.New(400, "TODO_NOT_MODIFIED")

func checkTodoItems(list []tg.TodoItem) error {
	seen := make([]int, 0, len(list))
	for _, it := range list {
		n := utf8.RuneCountInString(it.Title.Text)
		if n == 0 || n > TodoItemMax || it.ID <= 0 || slices.Contains(seen, it.ID) {
			return tgerr.New(400, "TODO_ITEM_INVALID")
		}
		seen = append(seen, it.ID)
	}
	return nil
}

// NewTodo checks a checklist a client sent, and keeps any ticks old has on
// items that are still there (editing a checklist doesn't undo them).
func NewTodo(in tg.TodoList, old *tg.MessageMediaToDo) (Ref, error) {
	if n := utf8.RuneCountInString(in.Title.Text); n == 0 || n > TodoTitleMax {
		return Ref{}, tgerr.New(400, "TODO_TITLE_INVALID")
	}
	if len(in.List) == 0 || len(in.List) > TodoItemsMax {
		return Ref{}, tgerr.New(400, "TODO_ITEMS_EMPTY")
	}
	if err := checkTodoItems(in.List); err != nil {
		return Ref{}, err
	}
	m := &tg.MessageMediaToDo{Todo: in}
	if old != nil {
		var keep []tg.TodoCompletion
		for _, c := range old.Completions {
			if slices.ContainsFunc(in.List, func(it tg.TodoItem) bool { return it.ID == c.ID }) {
				keep = append(keep, c)
			}
		}
		if len(keep) > 0 {
			m.SetCompletions(keep)
		}
	}
	return Ref{Kind: KindTodo, TL: m}, nil
}

// ToggleTodo applies ticks and unticks by peer to a checklist in place. It
// says whether anything changed.
func ToggleTodo(m *tg.MessageMediaToDo, by tg.PeerClass, completed, incompleted []int, date int) (bool, error) {
	has := func(id int) bool {
		return slices.ContainsFunc(m.Todo.List, func(it tg.TodoItem) bool { return it.ID == id })
	}
	changed := false
	list := make([]tg.TodoCompletion, 0, len(m.Completions)+len(completed))
	for _, c := range m.Completions {
		if slices.Contains(incompleted, c.ID) {
			changed = true
			continue
		}
		list = append(list, c)
	}
	for _, id := range completed {
		if !has(id) {
			return false, errTodoInvalid
		}
		if slices.ContainsFunc(list, func(c tg.TodoCompletion) bool { return c.ID == id }) {
			continue
		}
		list = append(list, tg.TodoCompletion{ID: id, CompletedBy: by, Date: date})
		changed = true
	}
	m.SetCompletions(list)
	return changed, nil
}

// AppendTodo adds items to a checklist in place.
func AppendTodo(m *tg.MessageMediaToDo, items []tg.TodoItem) error {
	if len(items) == 0 || len(m.Todo.List)+len(items) > TodoItemsMax {
		return tgerr.New(400, "TODO_ITEMS_EMPTY")
	}
	all := append(slices.Clone(m.Todo.List), items...)
	if err := checkTodoItems(all); err != nil {
		return err
	}
	m.Todo.List = all
	return nil
}
