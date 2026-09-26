/*
i decided to make it with go not c/c++
because i found it easier to make and think in it with channels + a more hands on experience with it

my idea is a one G read map and write on a channel
other G read from channel and collect a patch maybe 5000 event and wrie it to db in one transaction

anotehr stupid idea was each event asyn transsaction to db but i found it will be a lot of transaction and will be slow
+ db sometimes it's max of connections is maybe hundreds
soo it's a bad bad idea

it's possible with c but shared memory is sometimes hard and mutex porbs maybe idk but it's possible

dont worry about maps memory safty it's safe kernel handle that 
*/
package main
