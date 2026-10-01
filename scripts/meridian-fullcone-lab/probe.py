import socket,struct,os,json,sys
COOKIE=0x2112a442
control=socket.create_connection(('192.168.240.3',1080),3);control.sendall(b'\x05\x01\x00');assert control.recv(2)==b'\x05\x00'
control.sendall(b'\x05\x03\x00\x01\x00\x00\x00\x00\x00\x00');response=control.recv(32);assert response[:4]==b'\x05\x00\x00\x01',response
relay=(socket.inet_ntoa(response[4:8]),struct.unpack('!H',response[8:10])[0]);sock=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);sock.bind(('0.0.0.0',0));sock.settimeout(6)

def query(ip,port,change,expected):
 txid=os.urandom(12);attrs=struct.pack('!HHI',3,4,change) if change else b'';msg=struct.pack('!HHI',1,len(attrs),COOKIE)+txid+attrs
 header=b'\x00\x00\x00\x01'+socket.inet_aton(ip)+struct.pack('!H',port)
 sock.sendto(header+msg,relay);received,_=sock.recvfrom(2048)
 assert received[:4]==b'\x00\x00\x00\x01',received[:4]
 sender=(socket.inet_ntoa(received[4:8]),struct.unpack('!H',received[8:10])[0]);assert sender==expected,(sender,expected)
 data=received[10:];assert data[:2]==b'\x01\x01' and data[8:20]==txid
 mapped=None
 offset=20
 while offset<len(data):
  kind,length=struct.unpack('!HH',data[offset:offset+4]);value=data[offset+4:offset+4+length]
  if kind==0x20:
   mapped=(socket.inet_ntoa(struct.pack('!I',struct.unpack('!I',value[4:8])[0]^COOKIE)),struct.unpack('!H',value[2:4])[0]^(COOKIE>>16))
  offset+=4+((length+3)//4)*4
 assert mapped is not None
 return {'change':change,'reply':sender,'mapped':mapped}
results=[query('192.168.240.4',3478,0,('192.168.240.4',3478)),query('192.168.240.4',3478,6,('192.168.240.5',3479)),query('192.168.240.4',3478,2,('192.168.240.4',3479)),query('192.168.240.5',3479,0,('192.168.240.5',3479))]
assert len({tuple(r['mapped']) for r in results})==1,results
print(json.dumps({'protocol':sys.argv[1],'ordinary':True,'alternate_ip_port':True,'alternate_port':True,'endpoint_independent_mapping':True,'evidence':results}))
