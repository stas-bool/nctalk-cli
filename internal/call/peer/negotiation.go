package peer

import "github.com/pion/webrtc/v4"

// negotiation.go — perfect-negotiation: glare-resolution (спека §7).
//
// Glare возникает, когда оба пира одновременно в have-local-offer (оба позвали
// CreateOffer) — offer'ы летят навстречу друг другу. W3C perfect-negotiation
// решает это ролями polite/impolite, назначаемыми детерминированно по сравнению
// sessionId пиров (меньший = impolite, больший = polite — см. agent.addPeer):
//
//   - polite   откатывает свой offer (SetLocalDescription rollback), принимает
//     входящий и отвечает answer'ом;
//   - impolite игнорирует входящий offer — свой уже уйдёт и будет обработан
//     polite-стороной.
//
// Роли назначает вышестоящий слой (agent) по sessionId; сам peer хранит только
// флаг isPolite. FSM-состояния здесь — это pion SignalingState: отдельной
// машины состояний не нужно, glare детектируется по текущему состоянию ==
// have-local-offer в момент прихода входящего offer.

// incomingOfferAction — решение peer'а по входящему offer.
type incomingOfferAction int

const (
	// offerAccept — принять входящий offer: SetRemoteDescription(offer)
	// (с предварительным rollback, если peer в have-local-offer — polite при
	// glare) → CreateAnswer → SetLocalDescription. Используется и в обычном flow
	// (нет glare), и в glare-polite.
	offerAccept incomingOfferAction = iota
	// offerIgnore — проигнорировать входящий offer: impolite при glare (свой
	// offer в полёте, в приоритете). Ничего не делать, не менять состояние.
	offerIgnore
)

// resolveIncomingOffer решает, принять или проигнорировать входящий offer в
// зависимости от текущего signaling state и роли perfect-negotiation:
//
//   - state != have-local-offer (stable/have-remote-offer/…) — нет glare,
//     обычный приём → offerAccept;
//   - have-local-offer (glare):
//       polite   → offerAccept (вышестоящий код сделает rollback перед приёмом);
//       impolite → offerIgnore.
//
// Чистая функция без сайд-эффектов — тестируется независимо от pion.
func resolveIncomingOffer(state webrtc.SignalingState, isPolite bool) incomingOfferAction {
	if state != webrtc.SignalingStateHaveLocalOffer {
		return offerAccept
	}
	if isPolite {
		return offerAccept // glare: polite откатывает свой, принимает входящий
	}
	return offerIgnore // glare: impolite игнорирует входящий
}
