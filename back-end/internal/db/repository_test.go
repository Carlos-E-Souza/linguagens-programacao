package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/models"
)

// newTestStore devolve um Store sobre um banco novo já migrado.
func newTestStore(t *testing.T) *Store {
	t.Helper()

	handle := newTestDB(t)
	if err := Migrate(context.Background(), handle); err != nil {
		t.Fatalf("Migrate() failed: %v", err)
	}

	return NewStore(handle)
}

// mustCreateUser cadastra um usuário, falhando o teste em caso de erro.
func mustCreateUser(t *testing.T, s *Store, username string) models.User {
	t.Helper()

	user, err := s.CreateUser(context.Background(), username, "Display "+username, "hash-"+username)
	if err != nil {
		t.Fatalf("CreateUser(%q) failed: %v", username, err)
	}
	return user
}

// mustCreateDirect cria uma conversa individual entre dois usuários.
func mustCreateDirect(t *testing.T, s *Store, a, b models.User) models.Conversation {
	t.Helper()

	conv, err := s.CreateConversation(context.Background(),
		models.ConversationDirect, "", a.ID, []int64{a.ID, b.ID}, nil)
	if err != nil {
		t.Fatalf("CreateConversation(direct) failed: %v", err)
	}
	return conv
}

// mustCreateGroup cria uma conversa em grupo cujo dono é owner.
func mustCreateGroup(t *testing.T, s *Store, owner models.User, name string, members []int64) models.Conversation {
	t.Helper()

	roles := map[int64]string{owner.ID: models.RoleAdmin}
	ids := append([]int64{owner.ID}, members...)

	conv, err := s.CreateConversation(context.Background(),
		models.ConversationGroup, name, owner.ID, ids, roles)
	if err != nil {
		t.Fatalf("CreateConversation(group) failed: %v", err)
	}
	return conv
}

func TestCreateUserAndLookup(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	created := mustCreateUser(t, s, "gabriel")

	byID, err := s.GetUserByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetUserByID() failed: %v", err)
	}
	if got, want := byID.Username, "gabriel"; got != want {
		t.Errorf("username: got %q, want %q", got, want)
	}
	if got, want := byID.PasswordHash, "hash-gabriel"; got != want {
		t.Errorf("password hash: got %q, want %q", got, want)
	}

	byName, err := s.GetUserByUsername(ctx, "gabriel")
	if err != nil {
		t.Fatalf("GetUserByUsername() failed: %v", err)
	}
	if byName.ID != created.ID {
		t.Errorf("id: got %d, want %d", byName.ID, created.ID)
	}
}

func TestGetUserNotFound(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.GetUserByID(context.Background(), 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetUserByID(missing) error = %v, want ErrNotFound", err)
	}
}

// TestCreateUserRejectsDuplicateUsername prova que a restrição UNIQUE é a
// proteção real, e não um SELECT prévio sujeito a corrida.
func TestCreateUserRejectsDuplicateUsername(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	mustCreateUser(t, s, "gabriel")

	_, err := s.CreateUser(ctx, "gabriel", "Other", "hash")
	if !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate username error = %v, want ErrConflict", err)
	}
}

func TestSearchUsersExcludesCallerAndMatchesSubstring(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	me := mustCreateUser(t, s, "gabriel")
	mustCreateUser(t, s, "gabriela")
	mustCreateUser(t, s, "carlos")

	users, err := s.SearchUsers(ctx, "gabri", me.ID, 20)
	if err != nil {
		t.Fatalf("SearchUsers() failed: %v", err)
	}

	if got, want := len(users), 1; got != want {
		t.Fatalf("result count: got %d, want %d", got, want)
	}
	if got, want := users[0].Username, "gabriela"; got != want {
		t.Errorf("username: got %q, want %q", got, want)
	}
}

// TestSearchUsersEscapesWildcards é o caso interessante: o termo vem do
// usuário, então um % isolado precisa casar com um sinal de porcentagem
// literal em vez de com todas as linhas.
func TestSearchUsersEscapesWildcards(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	me := mustCreateUser(t, s, "gabriel")
	mustCreateUser(t, s, "carlos")
	mustCreateUser(t, s, "100%real")

	users, err := s.SearchUsers(ctx, "%", me.ID, 20)
	if err != nil {
		t.Fatalf("SearchUsers() failed: %v", err)
	}

	if got, want := len(users), 1; got != want {
		t.Fatalf("wildcard term matched %d users, want %d (escaping failed)", got, want)
	}
	if got, want := users[0].Username, "100%real"; got != want {
		t.Errorf("username: got %q, want %q", got, want)
	}
}

func TestCreateDirectConversationAndFindIt(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	a := mustCreateUser(t, s, "alice")
	b := mustCreateUser(t, s, "bob")

	created := mustCreateDirect(t, s, a, b)

	found, err := s.GetDirectConversation(ctx, a.ID, b.ID)
	if err != nil {
		t.Fatalf("GetDirectConversation() failed: %v", err)
	}
	if found.ID != created.ID {
		t.Errorf("conversation id: got %d, want %d", found.ID, created.ID)
	}

	// A busca precisa ser simétrica: de qualquer lado é a mesma conversa.
	reversed, err := s.GetDirectConversation(ctx, b.ID, a.ID)
	if err != nil {
		t.Fatalf("GetDirectConversation(reversed) failed: %v", err)
	}
	if reversed.ID != created.ID {
		t.Errorf("reversed lookup id: got %d, want %d", reversed.ID, created.ID)
	}
}

// TestGetDirectConversationIgnoresGroups protege a regra COUNT(*) = 2: um
// grupo com as mesmas duas pessoas não pode ser confundido com a conversa
// privada delas.
func TestGetDirectConversationIgnoresGroups(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	a := mustCreateUser(t, s, "alice")
	b := mustCreateUser(t, s, "bob")

	mustCreateGroup(t, s, a, "team", []int64{b.ID})

	if _, err := s.GetDirectConversation(ctx, a.ID, b.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetDirectConversation() error = %v, want ErrNotFound", err)
	}
}

func TestCreateConversationRollsBackOnMissingMember(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	a := mustCreateUser(t, s, "alice")

	_, err := s.CreateConversation(ctx, models.ConversationGroup, "ghosts",
		a.ID, []int64{a.ID, 4242}, map[int64]string{a.ID: models.RoleAdmin})
	if err == nil {
		t.Fatal("expected an error for a non-existent member")
	}

	// A transação que falhou não pode deixar nada para trás.
	var count int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM conversations`).Scan(&count); err != nil {
		t.Fatalf("count conversations: %v", err)
	}
	if got, want := count, 0; got != want {
		t.Errorf("conversations after rollback: got %d, want %d", got, want)
	}
}

func TestMembershipAndRoles(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	owner := mustCreateUser(t, s, "owner")
	member := mustCreateUser(t, s, "member")
	outsider := mustCreateUser(t, s, "outsider")

	group := mustCreateGroup(t, s, owner, "team", []int64{member.ID})

	isMember, err := s.IsMember(ctx, group.ID, member.ID)
	if err != nil {
		t.Fatalf("IsMember() failed: %v", err)
	}
	if !isMember {
		t.Error("member should be a member of the group")
	}

	isMember, err = s.IsMember(ctx, group.ID, outsider.ID)
	if err != nil {
		t.Fatalf("IsMember() failed: %v", err)
	}
	if isMember {
		t.Error("outsider must not be a member of the group")
	}

	role, err := s.GetMemberRole(ctx, group.ID, owner.ID)
	if err != nil {
		t.Fatalf("GetMemberRole() failed: %v", err)
	}
	if got, want := role, models.RoleAdmin; got != want {
		t.Errorf("owner role: got %q, want %q", got, want)
	}

	if _, err := s.GetMemberRole(ctx, group.ID, outsider.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetMemberRole(outsider) error = %v, want ErrNotFound", err)
	}
}

// TestAddMembersIsIdempotent cobre o caso de reconexão: a mesma requisição
// chegando duas vezes não pode falhar.
func TestAddMembersIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	owner := mustCreateUser(t, s, "owner")
	late := mustCreateUser(t, s, "late")
	group := mustCreateGroup(t, s, owner, "team", nil)

	for i := 0; i < 2; i++ {
		if err := s.AddMembers(ctx, group.ID, []int64{late.ID}); err != nil {
			t.Fatalf("AddMembers() attempt %d failed: %v", i+1, err)
		}
	}

	members, err := s.ListMembers(ctx, group.ID)
	if err != nil {
		t.Fatalf("ListMembers() failed: %v", err)
	}
	if got, want := len(members), 2; got != want {
		t.Errorf("member count: got %d, want %d", got, want)
	}
}

func TestRemoveMember(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	owner := mustCreateUser(t, s, "owner")
	member := mustCreateUser(t, s, "member")
	group := mustCreateGroup(t, s, owner, "team", []int64{member.ID})

	if err := s.RemoveMember(ctx, group.ID, member.ID); err != nil {
		t.Fatalf("RemoveMember() failed: %v", err)
	}

	if err := s.RemoveMember(ctx, group.ID, member.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second RemoveMember() error = %v, want ErrNotFound", err)
	}
}

func TestListContacts(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	alice := mustCreateUser(t, s, "alice")
	bob := mustCreateUser(t, s, "bob")
	carol := mustCreateUser(t, s, "carol")
	stranger := mustCreateUser(t, s, "stranger")

	mustCreateDirect(t, s, alice, bob)
	mustCreateGroup(t, s, alice, "team", []int64{carol.ID})

	contacts, err := s.ListContacts(ctx, alice.ID)
	if err != nil {
		t.Fatalf("ListContacts() failed: %v", err)
	}

	found := make(map[int64]bool, len(contacts))
	for _, id := range contacts {
		found[id] = true
	}

	if !found[bob.ID] || !found[carol.ID] {
		t.Errorf("contacts should include bob and carol, got %v", contacts)
	}
	if found[stranger.ID] {
		t.Error("stranger must not be a contact")
	}
	if found[alice.ID] {
		t.Error("the user must not be their own contact")
	}
}

func TestUnreadCountAndReadWatermark(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	alice := mustCreateUser(t, s, "alice")
	bob := mustCreateUser(t, s, "bob")
	conv := mustCreateDirect(t, s, alice, bob)

	inserted, err := s.InsertMessages(ctx, []models.Message{
		newMessage(conv.ID, 1, alice.ID, "c1", "one"),
		newMessage(conv.ID, 2, alice.ID, "c2", "two"),
		newMessage(conv.ID, 3, alice.ID, "c3", "three"),
	})
	if err != nil {
		t.Fatalf("InsertMessages() failed: %v", err)
	}
	if got, want := len(inserted), 3; got != want {
		t.Fatalf("inserted: got %d, want %d", got, want)
	}
	// As linhas devolvidas precisam trazer os IDs atribuídos pelo banco.
	for _, msg := range inserted {
		if msg.ID == 0 {
			t.Errorf("stored message %q has no ID", msg.ClientID)
		}
	}

	unreadFor := func(userID int64) int {
		t.Helper()
		convs, err := s.ListUserConversations(ctx, userID)
		if err != nil {
			t.Fatalf("ListUserConversations() failed: %v", err)
		}
		for _, c := range convs {
			if c.ID == conv.ID {
				return c.UnreadCount
			}
		}
		t.Fatalf("conversation %d not found for user %d", conv.ID, userID)
		return 0
	}

	if got, want := unreadFor(bob.ID), 3; got != want {
		t.Errorf("unread before reading: got %d, want %d", got, want)
	}
	// As mensagens do próprio remetente nunca contam como não lidas para ele.
	if got, want := unreadFor(alice.ID), 0; got != want {
		t.Errorf("unread for the sender: got %d, want %d", got, want)
	}

	if err := s.MarkRead(ctx, conv.ID, bob.ID, 2); err != nil {
		t.Fatalf("MarkRead() failed: %v", err)
	}

	if got, want := unreadFor(bob.ID), 1; got != want {
		t.Errorf("unread after reading up to 2: got %d, want %d", got, want)
	}
}

// TestMarkDeliveredIsMonotonic prova que uma confirmação fora de ordem não
// move a marca para trás.
func TestMarkDeliveredIsMonotonic(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	alice := mustCreateUser(t, s, "alice")
	bob := mustCreateUser(t, s, "bob")
	conv := mustCreateDirect(t, s, alice, bob)

	if err := s.MarkDelivered(ctx, conv.ID, bob.ID, 10); err != nil {
		t.Fatalf("MarkDelivered(10) failed: %v", err)
	}
	if err := s.MarkDelivered(ctx, conv.ID, bob.ID, 5); err != nil {
		t.Fatalf("MarkDelivered(5) failed: %v", err)
	}

	members, err := s.ListMembers(ctx, conv.ID)
	if err != nil {
		t.Fatalf("ListMembers() failed: %v", err)
	}
	for _, m := range members {
		if m.UserID != bob.ID {
			continue
		}
		if got, want := m.LastDelivered, int64(10); got != want {
			t.Errorf("last_delivered: got %d, want %d (watermark moved backwards)", got, want)
		}
	}
}

// TestMarkReadAlsoAdvancesDelivered documenta que ler implica entrega, algo
// de que o cliente depende para os seus ícones de confirmação.
func TestMarkReadAlsoAdvancesDelivered(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	alice := mustCreateUser(t, s, "alice")
	bob := mustCreateUser(t, s, "bob")
	conv := mustCreateDirect(t, s, alice, bob)

	if err := s.MarkRead(ctx, conv.ID, bob.ID, 7); err != nil {
		t.Fatalf("MarkRead() failed: %v", err)
	}

	members, err := s.ListMembers(ctx, conv.ID)
	if err != nil {
		t.Fatalf("ListMembers() failed: %v", err)
	}
	for _, m := range members {
		if m.UserID != bob.ID {
			continue
		}
		if got, want := m.LastRead, int64(7); got != want {
			t.Errorf("last_read: got %d, want %d", got, want)
		}
		if got, want := m.LastDelivered, int64(7); got != want {
			t.Errorf("last_delivered: got %d, want %d", got, want)
		}
	}
}

// TestInsertMessagesDeduplicatesByClientID é a garantia de que depende
// todo o fluxo de reenvio após reconexão.
func TestInsertMessagesDeduplicatesByClientID(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	alice := mustCreateUser(t, s, "alice")
	bob := mustCreateUser(t, s, "bob")
	conv := mustCreateDirect(t, s, alice, bob)

	msg := newMessage(conv.ID, 1, alice.ID, "same-client-id", "hello")

	inserted, err := s.InsertMessages(ctx, []models.Message{msg})
	if err != nil {
		t.Fatalf("first InsertMessages() failed: %v", err)
	}
	if got, want := len(inserted), 1; got != want {
		t.Fatalf("first insert: got %d, want %d", got, want)
	}

	inserted, err = s.InsertMessages(ctx, []models.Message{msg})
	if err != nil {
		t.Fatalf("second InsertMessages() failed: %v", err)
	}
	if got, want := len(inserted), 0; got != want {
		t.Errorf("duplicate insert: got %d, want %d", got, want)
	}

	var count int
	if err := s.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM messages WHERE conversation_id = ?`, conv.ID).Scan(&count); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if got, want := count, 1; got != want {
		t.Errorf("stored messages: got %d, want %d", got, want)
	}
}

func TestListMessagesPagination(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	alice := mustCreateUser(t, s, "alice")
	bob := mustCreateUser(t, s, "bob")
	conv := mustCreateDirect(t, s, alice, bob)

	batch := make([]models.Message, 0, 10)
	for i := 1; i <= 10; i++ {
		batch = append(batch, newMessage(conv.ID, int64(i), alice.ID,
			fmt.Sprintf("client-%d", i), fmt.Sprintf("msg %d", i)))
	}
	if _, err := s.InsertMessages(ctx, batch); err != nil {
		t.Fatalf("InsertMessages() failed: %v", err)
	}

	// Página mais nova: sem cursor, então as três últimas por sequence.
	page, err := s.ListMessages(ctx, conv.ID, 0, 3)
	if err != nil {
		t.Fatalf("ListMessages() failed: %v", err)
	}
	if got, want := len(page), 3; got != want {
		t.Fatalf("page size: got %d, want %d", got, want)
	}
	// A ordem crescente é o contrato para a tela de chat.
	if got, want := page[0].Sequence, int64(8); got != want {
		t.Errorf("first sequence: got %d, want %d", got, want)
	}
	if got, want := page[2].Sequence, int64(10); got != want {
		t.Errorf("last sequence: got %d, want %d", got, want)
	}

	// Página mais antiga: estritamente antes da sequence 8.
	older, err := s.ListMessages(ctx, conv.ID, 8, 3)
	if err != nil {
		t.Fatalf("ListMessages(before) failed: %v", err)
	}
	if got, want := len(older), 3; got != want {
		t.Fatalf("older page size: got %d, want %d", got, want)
	}
	if got, want := older[2].Sequence, int64(7); got != want {
		t.Errorf("older last sequence: got %d, want %d", got, want)
	}
}

func TestListMessagesAfter(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	alice := mustCreateUser(t, s, "alice")
	bob := mustCreateUser(t, s, "bob")
	conv := mustCreateDirect(t, s, alice, bob)

	batch := make([]models.Message, 0, 5)
	for i := 1; i <= 5; i++ {
		batch = append(batch, newMessage(conv.ID, int64(i), alice.ID,
			fmt.Sprintf("client-%d", i), "body"))
	}
	if _, err := s.InsertMessages(ctx, batch); err != nil {
		t.Fatalf("InsertMessages() failed: %v", err)
	}

	missed, err := s.ListMessagesAfter(ctx, conv.ID, 3, 100)
	if err != nil {
		t.Fatalf("ListMessagesAfter() failed: %v", err)
	}
	if got, want := len(missed), 2; got != want {
		t.Fatalf("missed count: got %d, want %d", got, want)
	}
	if got, want := missed[0].Sequence, int64(4); got != want {
		t.Errorf("first missed sequence: got %d, want %d", got, want)
	}
}

// TestLastSequenceTracksInserts cobre o hub inicializando seu contador a
// partir do banco depois de reiniciar.
func TestLastSequenceTracksInserts(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	alice := mustCreateUser(t, s, "alice")
	bob := mustCreateUser(t, s, "bob")
	conv := mustCreateDirect(t, s, alice, bob)

	seq, err := s.LastSequence(ctx, conv.ID)
	if err != nil {
		t.Fatalf("LastSequence() failed: %v", err)
	}
	if got, want := seq, int64(0); got != want {
		t.Errorf("initial last_sequence: got %d, want %d", got, want)
	}

	if _, err := s.InsertMessages(ctx, []models.Message{
		newMessage(conv.ID, 1, alice.ID, "c1", "one"),
		newMessage(conv.ID, 2, alice.ID, "c2", "two"),
	}); err != nil {
		t.Fatalf("InsertMessages() failed: %v", err)
	}

	seq, err = s.LastSequence(ctx, conv.ID)
	if err != nil {
		t.Fatalf("LastSequence() failed: %v", err)
	}
	if got, want := seq, int64(2); got != want {
		t.Errorf("last_sequence: got %d, want %d", got, want)
	}
}

// TestStatusUpdateFromNonMemberIsNotFound protege o caminho de
// autorização: uma gravação de status de alguém de fora da conversa
// precisa ser informada.
func TestStatusUpdateFromNonMemberIsNotFound(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	alice := mustCreateUser(t, s, "alice")
	bob := mustCreateUser(t, s, "bob")
	outsider := mustCreateUser(t, s, "outsider")
	conv := mustCreateDirect(t, s, alice, bob)

	if err := s.MarkRead(ctx, conv.ID, outsider.ID, 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("MarkRead(outsider) error = %v, want ErrNotFound", err)
	}
}

// newMessage monta uma mensagem com horário em UTC, para que os testes
// nunca dependam do relógio local.
func newMessage(conversationID, sequence, senderID int64, clientID, body string) models.Message {
	return models.Message{
		ConversationID: conversationID,
		Sequence:       sequence,
		SenderID:       senderID,
		ClientID:       clientID,
		Body:           body,
		CreatedAt:      time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}
}

// TestStoreAgainstSharedMemoryDatabase é um teste rápido da variante em
// memória, já que algumas restrições se comportam diferente sem WAL.
func TestStoreAgainstSharedMemoryDatabase(t *testing.T) {
	handle, err := sql.Open("sqlite", "file::memory:?cache=shared&_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatalf("open in-memory database: %v", err)
	}
	defer handle.Close()
	handle.SetMaxOpenConns(1)

	if err := Migrate(context.Background(), handle); err != nil {
		t.Fatalf("Migrate() failed: %v", err)
	}

	s := NewStore(handle)
	user, err := s.CreateUser(context.Background(), "memory", "Memory", "hash")
	if err != nil {
		t.Fatalf("CreateUser() failed: %v", err)
	}
	if user.ID == 0 {
		t.Error("expected a non-zero user id")
	}
}

// TestListUserConversationsLastMessageAndActivityOrder cobre as duas
// promessas da lista de conversas: cada item traz sua última mensagem, e a
// conversa com atividade mais recente vem primeiro, independentemente de
// quantas mensagens cada uma tem.
func TestListUserConversationsLastMessageAndActivityOrder(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	alice := mustCreateUser(t, s, "alice")
	bob := mustCreateUser(t, s, "bob")
	carol := mustCreateUser(t, s, "carol")

	busy := mustCreateDirect(t, s, alice, bob)     // muitas mensagens antigas
	recent := mustCreateDirect(t, s, alice, carol) // uma mensagem nova
	empty := mustCreateGroup(t, s, alice, "empty", []int64{bob.ID})

	base := time.Now().UTC().Add(time.Hour)
	at := func(m models.Message, offset time.Duration) models.Message {
		m.CreatedAt = base.Add(offset)
		return m
	}

	if _, err := s.InsertMessages(ctx, []models.Message{
		at(newMessage(busy.ID, 1, bob.ID, "b1", "old one"), 1*time.Second),
		at(newMessage(busy.ID, 2, bob.ID, "b2", "old two"), 2*time.Second),
		at(newMessage(busy.ID, 3, bob.ID, "b3", "old three"), 3*time.Second),
		at(newMessage(recent.ID, 1, carol.ID, "c1", "newest"), 10*time.Second),
	}); err != nil {
		t.Fatalf("InsertMessages() failed: %v", err)
	}

	convs, err := s.ListUserConversations(ctx, alice.ID)
	if err != nil {
		t.Fatalf("ListUserConversations() failed: %v", err)
	}
	if got, want := len(convs), 3; got != want {
		t.Fatalf("conversation count: got %d, want %d", got, want)
	}

	// recent tem a mensagem mais nova, busy as mais antigas, e o grupo
	// vazio só o horário de criação, que é anterior às duas.
	order := []int64{convs[0].ID, convs[1].ID, convs[2].ID}
	if want := []int64{recent.ID, busy.ID, empty.ID}; fmt.Sprint(order) != fmt.Sprint(want) {
		t.Errorf("order: got %v, want %v (recent, busy, empty)", order, want)
	}

	last := convs[1].LastMessage
	if last == nil {
		t.Fatal("busy conversation has no last_message")
	}
	if last.Sequence != 3 || last.Body != "old three" || last.ID == 0 {
		t.Errorf("busy last_message: got seq=%d body=%q id=%d, want seq=3 body=\"old three\" id>0",
			last.Sequence, last.Body, last.ID)
	}
	if !last.CreatedAt.Equal(base.Add(3 * time.Second)) {
		t.Errorf("busy last_message created_at: got %v, want %v", last.CreatedAt, base.Add(3*time.Second))
	}

	if convs[2].LastMessage != nil {
		t.Errorf("empty conversation should have no last_message, got %+v", convs[2].LastMessage)
	}
}

// TestApplyReceiptsBatch cobre a gravação em lote das marcas: ela é
// monotônica, ler implica entrega, e um membro que já saiu é pulado sem
// derrubar o lote.
func TestApplyReceiptsBatch(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	alice := mustCreateUser(t, s, "alice")
	bob := mustCreateUser(t, s, "bob")
	carol := mustCreateUser(t, s, "carol")
	group := mustCreateGroup(t, s, alice, "team", []int64{bob.ID, carol.ID})

	if err := s.MarkDelivered(ctx, group.ID, bob.ID, 10); err != nil {
		t.Fatalf("MarkDelivered() failed: %v", err)
	}
	if err := s.RemoveMember(ctx, group.ID, carol.ID); err != nil {
		t.Fatalf("RemoveMember() failed: %v", err)
	}

	err := s.ApplyReceipts(ctx, []Receipt{
		{ConversationID: group.ID, UserID: bob.ID, Delivered: 4},           // mais antiga: ignorada
		{ConversationID: group.ID, UserID: alice.ID, Read: 7},              // leitura implica entrega
		{ConversationID: group.ID, UserID: carol.ID, Delivered: 3},         // não é mais membro
		{ConversationID: group.ID, UserID: bob.ID, Delivered: 12, Read: 2}, // avança as duas
	})
	if err != nil {
		t.Fatalf("ApplyReceipts() failed: %v", err)
	}

	members, err := s.ListMembers(ctx, group.ID)
	if err != nil {
		t.Fatalf("ListMembers() failed: %v", err)
	}

	want := map[int64][2]int64{alice.ID: {7, 7}, bob.ID: {12, 2}}
	for _, m := range members {
		w := want[m.UserID]
		if m.LastDelivered != w[0] || m.LastRead != w[1] {
			t.Errorf("user %d: got delivered=%d read=%d, want delivered=%d read=%d",
				m.UserID, m.LastDelivered, m.LastRead, w[0], w[1])
		}
	}
}
