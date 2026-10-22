package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// profile.go — профиль пользователя (спека-дельта 2026-10-22 `users show`,
// §3–4): GET /ocs/v2.php/profile/{userId} — profile-API ЯДРА Nextcloud (не
// spreed; маршрут есть с Nextcloud 31). Своя группа эндпоинта — по аналогии с
// participants.go.

// pathProfile — profile-API ядра (спека-дельта 2026-10-22 §3). Живёт здесь, а
// не в paths.go: эндпоинт не принадлежит spreed (прецедент — pathCloudUser в
// files.go).
const pathProfile = "/ocs/v2.php/profile"

// Profile — каноническое представление профиля пользователя (спека-дельта
// 2026-10-22 §2–4). Сырой ответ НЕ проксируется: имена полей — camelCase как
// в остальных моделях (displayName, хотя сервер шлёт displayname); скрытые и
// незаполненные поля — "" (сервер шлёт null); Contacts всегда non-nil.
type Profile struct {
	UserId         string           `json:"userId"` // канонический uid из ответа, НЕ эхо аргумента
	DisplayName    string           `json:"displayName"`
	Role           string           `json:"role"`
	Headline       string           `json:"headline"`
	Organisation   string           `json:"organisation"`
	Address        string           `json:"address"`
	Pronouns       string           `json:"pronouns"`
	Biography      string           `json:"biography"`
	Timezone       string           `json:"timezone"`       // IANA-имя, клиент не интерпретирует
	TimezoneOffset int              `json:"timezoneOffset"` // секунды от UTC на момент запроса
	Contacts       []ProfileContact `json:"contacts"`
}

// ProfileContact — контакт из actions профиля (спека-дельта §2): служебные
// talk/talk-call отброшены, префиксы mailto:/tel: срезаны; Type и Value
// непустые после strings.TrimSpace (текст и --json выводят одни контакты).
type ProfileContact struct {
	Type  string `json:"type"`  // action id: email / phone / website / twitter / ...
	Value string `json:"value"` // target без mailto:/tel:
}

// rawProfile — ocs.data profile-API с серверными именами полей (спека-дельта
// §3). null строкового поля декодируется в "" (json.Unmarshal не трогает
// string на null); isUserAvatarVisible и icon/title action-ов не декодируются.
type rawProfile struct {
	UserId         string             `json:"userId"`
	DisplayName    string             `json:"displayname"`
	Role           string             `json:"role"`
	Headline       string             `json:"headline"`
	Organisation   string             `json:"organisation"`
	Address        string             `json:"address"`
	Pronouns       string             `json:"pronouns"`
	Biography      string             `json:"biography"`
	Actions        []rawProfileAction `json:"actions"`
	Timezone       string             `json:"timezone"`
	TimezoneOffset int                `json:"timezoneOffset"`
}

// rawProfileAction — элемент actions: {id, icon, title, target}; нужны id и target.
type rawProfileAction struct {
	Id     string `json:"id"`
	Target string `json:"target"`
}

// profileServiceActions — action-ы, НЕ являющиеся контактами (спека-дельта
// §2): ссылки «написать/позвонить в Talk».
var profileServiceActions = map[string]bool{
	"talk":      true,
	"talk-call": true,
}

// GetProfile возвращает профиль пользователя userId (спека-дельта 2026-10-22
// §3–4). Read-only GET.
//
// Guard до сети: userId обязан быть ОДНИМ сегментом пути (isPlainSegment —
// единственная проверка, cli её не дублирует): иначе «a/b» ушёл бы на чужой
// путь /profile/a/b, а «..» схлопнулся бы path.Join-ом транспорта в
// /ocs/v2.php. Управляющие символы не проверяются — сериализация URL
// экранирует их внутри сегмента (%0A).
//
// Запрос: GET pathProfile + "/" + userId — userId в путь КАК ЕСТЬ: эскейп
// сегмента делает transport.DoOCS (инвариант GetParticipants: ручной
// PathEscape дал бы двойной эскейп).
//
// Разбор — в УКАЗАТЕЛЬ: raw == nil после успешного doOCS означает, что данных
// нет (data:null, нет поля data, HTTP 204/304) — это drift формата, ошибка,
// а не пустой профиль с exit 0. Пустой userId в ответе — тоже drift.
//
// OCS-ошибка (*OCSError: 404 — нет пользователя/недоступен/нет маршрута,
// 400 — профиль отключён) возвращается КАК ЕСТЬ, без обёртки: тексты ошибок
// и exit-коды задаёт cli по Code/Message.
func (c *TalkClient) GetProfile(ctx context.Context, userId string) (*Profile, error) {
	if !isPlainSegment(userId) {
		return nil, fmt.Errorf("client: GetProfile: недопустимый userId %q (пустой, «.», «..» или содержит «/»)", userId)
	}

	var raw *rawProfile
	if _, err := c.doOCS(ctx, http.MethodGet, pathProfile+"/"+userId, nil, nil, false, &raw); err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, errors.New("client: GetProfile: пустой ответ сервера (нет ocs.data)")
	}
	if raw.UserId == "" {
		return nil, errors.New("client: GetProfile: в ответе сервера нет userId (drift формата)")
	}

	p := &Profile{
		UserId:         raw.UserId,
		DisplayName:    raw.DisplayName,
		Role:           raw.Role,
		Headline:       raw.Headline,
		Organisation:   raw.Organisation,
		Address:        raw.Address,
		Pronouns:       raw.Pronouns,
		Biography:      raw.Biography,
		Timezone:       raw.Timezone,
		TimezoneOffset: raw.TimezoneOffset,
		Contacts:       []ProfileContact{},
	}
	for _, a := range raw.Actions {
		if profileServiceActions[a.Id] {
			continue
		}
		v := contactValue(a.Target)
		// Пустой target (или голый «mailto:»), значение из одних пробелов или
		// пустой/пробельный id — контакта нет. Пробельные — тоже: рендер
		// схлопывает пробелы и пропускает строку с пустой меткой или значением,
		// и без этого фильтра --json отдал бы контакт, которого нет в тексте.
		if strings.TrimSpace(v) == "" || strings.TrimSpace(a.Id) == "" {
			continue
		}
		p.Contacts = append(p.Contacts, ProfileContact{Type: a.Id, Value: v})
	}
	return p, nil
}

// contactValue — target action-а без схемы mailto:/tel: (спека-дельта §2);
// прочие target (URL сайта, соцсетей) — как есть.
func contactValue(target string) string {
	for _, prefix := range []string{"mailto:", "tel:"} {
		if strings.HasPrefix(target, prefix) {
			return strings.TrimPrefix(target, prefix)
		}
	}
	return target
}
