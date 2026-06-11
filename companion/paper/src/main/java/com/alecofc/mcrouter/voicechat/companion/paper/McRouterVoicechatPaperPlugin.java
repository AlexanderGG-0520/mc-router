package com.alecofc.mcrouter.voicechat.companion.paper;

import com.alecofc.mcrouter.voicechat.companion.CompanionConfig;
import com.alecofc.mcrouter.voicechat.companion.CompanionVoicechatPlugin;
import com.alecofc.mcrouter.voicechat.companion.VoiceChatRegistrationManager;
import de.maxhenkel.voicechat.api.BukkitVoicechatService;
import org.bukkit.Bukkit;
import org.bukkit.plugin.RegisteredServiceProvider;
import org.bukkit.plugin.java.JavaPlugin;

public final class McRouterVoicechatPaperPlugin extends JavaPlugin {
    private VoiceChatRegistrationManager registrations;

    @Override
    public void onEnable() {
        CompanionConfig config = CompanionConfig.fromEnvironment();
        registrations = new VoiceChatRegistrationManager(config);

        RegisteredServiceProvider<BukkitVoicechatService> provider = Bukkit.getServicesManager().getRegistration(BukkitVoicechatService.class);
        if (provider == null) {
            throw new IllegalStateException("Simple Voice Chat Bukkit service is not available");
        }
        provider.getProvider().registerPlugin(new CompanionVoicechatPlugin(registrations, publicVoiceHost()));
        getLogger().info("Registered mc-router Simple Voice Chat companion for backend " + config.backendId());
    }

    @Override
    public void onDisable() {
        if (registrations != null) {
            registrations.close();
        }
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
