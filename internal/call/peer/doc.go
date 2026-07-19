// Package peer оборачивает pion/webrtc: создание PeerConnection на каждого
// удалённого участника, связывание SDP offer/answer и ICE-candidates со
// signaling, создание исходящего audio-track, подписка на входящие треки
// (OnTrack), perfect-negotiation. Спека 2026-07-19 §7.
package peer
