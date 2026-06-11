package com.alecofc.mcrouter.voicechat.companion.fabric;

import com.alecofc.mcrouter.voicechat.companion.CompanionConfig;
import com.alecofc.mcrouter.voicechat.companion.CompanionVoicechatPlugin;
import com.alecofc.mcrouter.voicechat.companion.VoiceChatRegistrationManager;
import de.maxhenkel.voicechat.api.VoicechatPlugin;
import de.maxhenkel.voicechat.api.events.EventRegistration;
import net.fabricmc.api.DedicatedServerModInitializer;

public final class McRouterVoicechatFabricMod implements DedicatedServerModInitializer, VoicechatPlugin {
    private static VoiceChatRegistrationManager registrations;
    private static CompanionVoicechatPlugin voicechatPlugin;

    @Override
    public void onInitializeServer() {
        ensureInitialized();
        Runtime.getRuntime().addShutdownHook(new Thread(() -> {
            if (registrations != null) {
                registrations.close();
            }
        }, "mc-router-voicechat-companion-shutdown"));
    }

    @Override
    public String getPluginId() {
        return "mc-router-voicechat-companion";
    }

    @Override
    public void registerEvents(EventRegistration registration) {
        ensureInitialized();
        voicechatPlugin.registerEvents(registration);
    }

    private static synchronized void ensureInitialized() {
        if (registrations != null) {
            return;
        }
        CompanionConfig config = CompanionConfig.fromEnvironment();
        registrations = new VoiceChatRegistrationManager(config);
        voicechatPlugin = new CompanionVoicechatPlugin(registrations, publicVoiceHost());
    }
    
    private static String publicVoiceHost() {
        String value = System.getenv("MC_ROUTER_VOICECHAT_PUBLIC_HOST");
        if (value == null || value.isBlank()) {
            throw new IllegalStateException(
                    "MC_ROUTER_VOICECHAT_PUBLIC_HOST must not be empty"
            );
        }
        return value;
    }
}
