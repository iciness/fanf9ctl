package main

// 模拟链路：没有硬件时也能把 UI 和协议流程跑通。
// 行为照着实测日志来：状态回 8 字节、设置帧原样回显、握手回 AA 55。

import (
	"sync"
	"time"
)

type simLink struct {
	mu     sync.Mutex
	inbox  chan []byte
	closed chan struct{}
	once   sync.Once
	rpm    int
	speed  byte
	aiMode bool
	step   int
}

func newSimLink() *simLink {
	s := &simLink{
		inbox:  make(chan []byte, 32),
		closed: make(chan struct{}),
		rpm:    1400,
		speed:  1,
	}
	go s.tick()
	return s
}

// tick 每 3 秒微调一次转速，模拟真实波动。
func (s *simLink) tick() {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.closed:
			return
		case <-t.C:
			s.mu.Lock()
			if s.aiMode {
				// AI 模式在 1100~2300 之间缓慢摆动
				s.step++
				base := []int{1400, 1600, 1850, 2100, 1900, 1650}
				s.rpm = base[s.step%len(base)]
			} else {
				s.rpm = []int{900, 1400, 1900, 2300}[int(s.speed)%4]
				s.rpm += (s.step % 7) * 3
				s.step++
			}
			s.mu.Unlock()
		}
	}
}

func (s *simLink) Write(b []byte) error {
	select {
	case <-s.closed:
		return nil
	default:
	}
	if len(b) < 3 || b[0] != frameHdr {
		return nil
	}
	cmd, sub := b[1], b[2]
	switch {
	case cmd == cmdStatus && sub == subStatus:
		go func() {
			time.Sleep(20 * time.Millisecond)
			s.push(s.statusFrame())
		}()
	case cmd == cmdSet && sub == subSet:
		// 记录档位，便于模拟器给出合理转速
		s.mu.Lock()
		if len(b) > 8 {
			s.aiMode = b[7] == 0
			s.speed = b[8]
		}
		s.mu.Unlock()
		echo := append([]byte(nil), b...)
		go func() {
			time.Sleep(15 * time.Millisecond)
			s.push(echo)
		}()
	case cmd == cmdRead && sub == subRead:
		reply := NewFrame(cmdSet, subSet, 0, []byte{
			0x00, 0x03, 0x01, 0x02, 0x00, 0x00, 0xff, 0xff, 0xff, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00})
		go func() {
			time.Sleep(15 * time.Millisecond)
			s.push(reply[:])
		}()
	case cmd == cmdInfo && sub == subInfo:
		reply := []byte{0xee, 0x03, 0x22, 0x00, 0x00, 0xaa, 0x55, 0x00, 0x00, 0x06, 0x80, 0x18, 0x00, 0x02, 0x08, 0x01, 0x00}
		go func() {
			time.Sleep(10 * time.Millisecond)
			s.push(reply)
		}()
	}
	return nil
}

func (s *simLink) statusFrame() []byte {
	s.mu.Lock()
	rpm := s.rpm
	s.mu.Unlock()
	v := uint16(rpm) * 4
	f := NewFrame(cmdStatus, subStatus, 0, nil)
	f[5] = 0x01
	f[6] = byte(v)
	f[7] = byte(v >> 8)
	return f[:]
}

func (s *simLink) push(b []byte) {
	select {
	case <-s.closed:
	case s.inbox <- b:
	default:
	}
}

func (s *simLink) Notifications() <-chan []byte { return s.inbox }

func (s *simLink) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

func (s *simLink) Describe() LinkInfo {
	return LinkInfo{
		Address: "00:00:00:00:00:00",
		Write:   "模拟写特征",
		Notify:  "模拟通知特征",
		Chars: []CharInfo{
			{Service: "sim", UUID: "sim-write", Props: "write", Writable: true},
			{Service: "sim", UUID: "sim-notify", Props: "notify", Notified: true},
		},
	}
}
