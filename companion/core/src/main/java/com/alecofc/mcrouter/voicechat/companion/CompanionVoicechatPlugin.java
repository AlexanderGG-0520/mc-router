package com.alecofc.mcrouter.voicechat.companion;

import de.maxhenkel.voicechat.api.VoicechatPlugin;
import de.maxhenkel.voicechat.api.events.EventRegistration;
import de.maxhenkel.voicechat.api.events.PlayerDisconnectedEvent;
import de.maxhenkel.voicechat.api.events.VoiceHostEvent;

import java.util.UUID;
import java.util.logging.Level;
import java.util.logging.Logger;

public final class CompanionVoicechatPlugin implements VoicechatPlugin {
    private static final Logger LOGGER = Logger.getLogger(CompanionVoicechatPlugin.class.getName());

    private final VoiceChatRegistrationManager registrations;
    private final String publicVoiceHost;

    public CompanionVoicechatPlugin(VoiceChatRegistrationManager registrations, String publicVoiceHost) {
        this.registrations = registrations;
        this.publicVoiceHost = publicVoiceHost;
    }

    @Override
    public String getPluginId() {
        return "mc-router-voicechat-companion";
    }

    @Override
    public void registerEvents(EventRegistration registration) {
        registration.registerEvent(VoiceHostEvent.class, this::onVoiceHost);
        registration.registerEvent(PlayerDisconnectedEvent.class, this::onPlayerDisconnected);
    }

    private void onVoiceHost(VoiceHostEvent event) {
        UUID playerUuid = event.getPlayer().getUuid();
        try {
            registrations.registerBeforeUdp(playerUuid);
            event.setVoiceHost(publicVoiceHost);
        } catch (Exception e) {
            LOGGER.log(Level.WARNING, "voicechat pre-UDP registration failed", e);
            event.setVoiceHost(publicVoiceHost);
        }
    }

    private void onPlayerDisconnected(PlayerDisconnectedEvent event) {
        registrations.unregister(event.getPlayerUuid());
    }
}
