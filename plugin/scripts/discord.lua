local msg = require("mp.msg")
local opts = require("mp.options")
local utils = require("mp.utils")

local options = {
	key = "D",
	active = true,
	client_id = "1476395038555640050",
	autohide_threshold = 0,
}

opts.read_options(options, "discord")

local version = "1.0.0"
msg.info(("plex-discord-rpc v%s edited by Glitchtest51"):format(version))

local cmd = nil

local function file_exists(path)
	local f = io.open(path, "r")
	if f then
		f:close()
		return true
	end
	return false
end

local script_dir = mp.get_script_directory() or debug.getinfo(1, "S").source:match("^@(.+[\\/])")
local is_windows = file_exists(script_dir .. "plex-discord-rpc.exe")
local binary_name = is_windows and "plex-discord-rpc.exe" or "plex-discord-rpc"
local binary_path = script_dir .. binary_name

local socket_name = "plexdiscordsocket"
local mpv_socket = is_windows and ("\\\\.\\pipe\\" .. socket_name) or ("/tmp/" .. socket_name)

if not file_exists(binary_path) then
	msg.fatal("Binary not found: " .. binary_path)
	os.exit(1)
end

msg.info(("ipc-path: %s"):format(mpv_socket))
mp.set_property("input-ipc-server", mpv_socket)

local function start()
	if cmd == nil then
		cmd = mp.command_native_async({
			name = "subprocess",
			playback_only = false,
			args = {binary_path, mpv_socket, options.client_id},
		}, function(success, result)
			msg.info("subprocess exited, success: " .. tostring(success))
			if result then
				msg.info("return code: " .. tostring(result.status))
			end
		end)
		msg.info("launching: " .. binary_path)
		mp.osd_message("Discord RPC: Started")
	end
end

local function stop()
	mp.abort_async_command(cmd)
	cmd = nil
	msg.info("aborted subprocess")
	mp.osd_message("Discord RPC: Stopped")
end

if options.active then
	mp.register_event("file-loaded", start)
	if mp.get_property("filename") ~= nil then
		start()
	end
end

mp.add_key_binding(options.key, "toggle-discord", function()
	if cmd ~= nil then
		stop()
	else
		start()
	end
end)

mp.register_event("shutdown", function()
	if cmd ~= nil then
		stop()
	end
	os.remove(mpv_socket)
end)

if options.autohide_threshold > 0 then
	local timer = nil
	local t = options.autohide_threshold
	mp.observe_property("pause", "bool", function(_, value)
		if value == true then
			timer = mp.add_timeout(t, function()
				if cmd ~= nil then
					stop()
				end
			end)
		else
			if timer ~= nil then
				timer:kill()
				timer = nil
			end
			if options.active and cmd == nil then
				start()
			end
		end
	end)
end
