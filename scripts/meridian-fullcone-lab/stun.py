import socket, struct, threading, ssl, json, time
COOKIE=0x2112a442
sockets={}
for ip in ('192.168.240.4','192.168.240.5'):
 for port in (3478,3479):
  s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);s.bind((ip,port));sockets[(ip,port)]=s

def address(kind, value, xor=False):
 ip,port=value;ipnum=struct.unpack('!I',socket.inet_aton(ip))[0]
 if xor: port^=COOKIE>>16;ipnum^=COOKIE
 return struct.pack('!HHBBHI',kind,8,0,1,port,ipnum)
def run(s):
 while True:
  msg,source=s.recvfrom(2048)
  if len(msg)<20 or struct.unpack('!HI',msg[2:8])[1]!=COOKIE:continue
  flags=0
  for i in range(20,len(msg),8):
   if msg[i:i+4]==b'\x00\x03\x00\x04':flags=struct.unpack('!I',msg[i+4:i+8])[0]
  ip,port=s.getsockname();reply=(('192.168.240.5' if ip=='192.168.240.4' else '192.168.240.4') if flags&4 else ip, (3479 if port==3478 else 3478) if flags&2 else port)
  body=address(0x20,source,True)+address(0x802b,reply)+address(0x802c,('192.168.240.5',3479))
  sockets[reply].sendto(struct.pack('!HHI',0x101,len(body),COOKIE)+msg[8:20]+body,source)
  print(json.dumps({'request':s.getsockname(),'source':source,'response':reply,'change':flags}),flush=True)
for s in sockets.values():threading.Thread(target=run,args=(s,),daemon=True).start()
ctx=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER);ctx.minimum_version=ssl.TLSVersion.TLSv1_3;ctx.set_alpn_protocols(['h2','http/1.1']);ctx.load_cert_chain('/lab/cert.pem','/lab/key.pem')
server=socket.socket();server.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1);server.bind(('192.168.240.4',443));server.listen()
print('ready',flush=True)
def tls(c):
 try:
  with ctx.wrap_socket(c,server_side=True) as tls:tls.recv(4096)
 except Exception as e: print(type(e).__name__,str(e),flush=True);c.close()
while True:
 c,_=server.accept();threading.Thread(target=tls,args=(c,),daemon=True).start()
